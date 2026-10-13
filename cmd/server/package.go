package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

const packageUsage = `uso: patchd-server package <comando> [opções]

comandos:
  show   mostra o instalador que o patchd usaria (Aula 14.1); nada é baixado
  fetch  baixa o instalador, confere o SHA-256 do manifesto e guarda (Aula 14.2)
  list   lista os instaladores guardados

show e fetch:
  -app ID        a regra do manifesto de terceiros (ex.: 7zip, google-chrome)
  -all           todas as regras do Windows com "install"
  -version V     a versão no winget-pkgs (padrão: a mais nova; pula a API do GitHub)
  -arch A        x64 (padrão), x86 ou arm64
  -type T        exige um tipo de instalador, no lugar do da regra
show:
  -file ARQUIVO  um manifesto de instalador local (.installer.yaml), sem rede
list:
  -verify        recalcula o SHA-256 de cada arquivo (lê todos do disco)

O manifesto de terceiros é o embutido, mais PATCHD_THIRDPARTY_MANIFEST se definido. fetch e
list usam o PostgreSQL e guardam em PATCHD_CONTENT_DIR/pacotes.
`

// packageStore: o que os comandos usam do armazenamento.
type packageStore interface {
	SaveThirdPartyPackage(ctx context.Context, p store.ThirdPartyPackage) (bool, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
}

// packagesDir: a pasta dos instaladores, dentro da pasta de conteúdo. O wsusscn2 fica na
// raiz dela e só apaga os próprios arquivos (wsusscn2-*.cab).
func packagesDir(contentDir string) string { return filepath.Join(contentDir, "pacotes") }

func runPackage(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains([]string{"show", "fetch", "list"}, args[0]) {
		fmt.Fprint(stderr, packageUsage)
		return exitConfig
	}
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	manifest, err := thirdparty.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	ua := "patchd-server/" + buildinfo.Get().Version
	c := winget.NewClient(ua)
	c.HTTP = retry.Wrap(c.HTTP, nil)
	if args[0] == "show" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return packageShow(ctx, c, manifest, args[1:], stdout, stderr)
	}

	dir, err := contentDir(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	if dir == "" {
		fmt.Fprintln(stderr, "patchd-server: defina PATCHD_CONTENT_DIR (os instaladores ficam em PATCHD_CONTENT_DIR/pacotes)")
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os pacotes exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour) // vários instaladores grandes
	defer cancel()
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	st := store.NewPostgres(pool)
	if args[0] == "list" {
		return packageList(ctx, st, packagesDir(dir), args[1:], time.Now(), stdout, stderr)
	}
	return packageFetch(ctx, c, pkgcache.NewFetcher(ua), st, manifest, packagesDir(dir), args[1:], stdout, stderr)
}

// packageFlags: as opções comuns de show e fetch.
type packageFlags struct {
	app, file, ver, arch, typ string
	all                       bool
}

func parsePackageFlags(name string, args []string, withFile bool, stderr io.Writer) (packageFlags, bool) {
	var o packageFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	fs.StringVar(&o.app, "app", "", "")
	fs.BoolVar(&o.all, "all", false, "")
	if withFile {
		fs.StringVar(&o.file, "file", "", "")
	}
	fs.StringVar(&o.ver, "version", "", "")
	fs.StringVar(&o.arch, "arch", "x64", "")
	fs.StringVar(&o.typ, "type", "", "")
	if err := fs.Parse(args); err != nil {
		return o, false
	}
	which := "-app e -all"
	if withFile {
		which = "-app, -all e -file"
	}
	switch {
	case btoi(o.app != "")+btoi(o.all)+btoi(o.file != "") != 1 || fs.NArg() > 0:
		fmt.Fprintf(stderr, "patchd-server: use um (e só um) de %s\n", which)
	case o.typ != "" && !winget.Supported(o.typ):
		fmt.Fprintf(stderr, "patchd-server: -type %q: use msi, wix, nullsoft, inno, burn ou exe\n", o.typ)
	case o.all && o.ver != "":
		fmt.Fprintln(stderr, "patchd-server: -version vale para um programa só (-app)")
	default:
		return o, true
	}
	return o, false
}

// rulesFor: as regras do Windows com install pedidas por -app ou -all.
func rulesFor(manifest thirdparty.Manifest, o packageFlags, stderr io.Writer) []thirdparty.App {
	var rules []thirdparty.App
	for _, a := range manifest.For("windows") {
		if a.Install != nil && (o.all || a.ID == o.app) {
			rules = append(rules, a)
		}
	}
	if len(rules) == 0 {
		if o.all {
			fmt.Fprintln(stderr, "patchd-server: nenhuma regra do Windows com \"install\" no manifesto")
		} else {
			fmt.Fprintf(stderr, "patchd-server: a regra %q do Windows não existe ou não tem \"install\" (veja catalog terceiros)\n", o.app)
		}
	}
	return rules
}

// resolve lê o manifesto do winget da regra (a versão pedida ou a mais nova).
func resolve(ctx context.Context, c *winget.Client, a thirdparty.App, ver string) (winget.Manifest, error) {
	if ver == "" {
		versions, err := c.Versions(ctx, a.Install.Winget)
		if err != nil {
			return winget.Manifest{}, err
		}
		ver = versions[0]
	}
	return c.Installer(ctx, a.Install.Winget, ver)
}

func wantFor(a thirdparty.App, o packageFlags) winget.Want {
	w := winget.Want{Arch: o.arch, Type: a.Install.Type}
	if o.typ != "" {
		w.Type = o.typ
	}
	return w
}

// --- show ---------------------------------------------------------------------------

func packageShow(ctx context.Context, c *winget.Client, manifest thirdparty.Manifest, args []string, stdout, stderr io.Writer) int {
	o, ok := parsePackageFlags("package show", args, true, stderr)
	if !ok {
		return exitConfig
	}
	if o.file != "" {
		data, err := os.ReadFile(o.file)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		m, err := winget.ParseInstaller(data)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %s: %v\n", o.file, err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%s %s (arquivo local)\n", m.PackageIdentifier, m.PackageVersion)
		if !printSelection(m, winget.Want{Arch: o.arch, Type: o.typ}, stdout) {
			return exitRuntime
		}
		return exitOK
	}
	rules := rulesFor(manifest, o, stderr)
	if len(rules) == 0 {
		return exitConfig
	}
	failed := false
	for i, a := range rules {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		m, err := resolve(ctx, c, a, o.ver)
		if err != nil {
			fmt.Fprintf(stdout, "%s (%s) ← winget %s\n  ERRO: %v\n", a.Name, a.ID, a.Install.Winget, err)
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s (%s) ← winget %s %s\n", a.Name, a.ID, m.PackageIdentifier, m.PackageVersion)
		if !printSelection(m, wantFor(a, o), stdout) {
			failed = true
		}
	}
	if failed {
		return exitRuntime
	}
	return exitOK
}

// printSelection mostra o instalador escolhido e os descartados; false se nenhum serve.
func printSelection(m winget.Manifest, w winget.Want, out io.Writer) bool {
	in, skipped, err := m.Select(w)
	if err != nil {
		fmt.Fprintf(out, "  ERRO: %v\n", err)
	} else {
		scope := in.Scope
		if scope == "" {
			scope = "não declarado"
		}
		fmt.Fprintf(out, "  instalador  #%d %s · %s · escopo %s\n", in.Index+1, in.Type, in.Architecture, scope)
		fmt.Fprintf(out, "  baixar de   %s\n", in.URL)
		fmt.Fprintf(out, "  sha-256     %s\n", in.SHA256)
		fmt.Fprintf(out, "  comando     %s\n", in.CommandLine("<arquivo>"))
		fmt.Fprintf(out, "  saída       %s\n", outcomes(in))
	}
	for _, s := range skipped {
		fmt.Fprintf(out, "  descartado  #%d %s · %s: %s\n", s.Installer.Index+1, s.Installer.Type, s.Installer.Architecture, s.Reason)
	}
	return err == nil
}

// outcomes resume os códigos de saída que não são falha: os do manifesto e, no MSI, os do
// Windows Installer.
func outcomes(in winget.Installer) string {
	codes := []int{0, 3010, 1641, 1618}
	for c := range in.ReturnCodes {
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	slices.Sort(codes[1:])
	var parts []string
	failures := 0
	for _, c := range codes {
		o, resp := in.Outcome(c)
		switch {
		case o == winget.OutcomeFailed:
			if _, declared := in.ReturnCodes[c]; declared {
				failures++
			}
		case resp == "":
			parts = append(parts, fmt.Sprintf("%d %s", c, o))
		default:
			parts = append(parts, fmt.Sprintf("%d %s (%s)", c, o, resp))
		}
	}
	s := strings.Join(parts, " · ")
	if failures > 0 {
		s += fmt.Sprintf(" · mais %d códigos de falha declarados", failures)
	}
	return s + " · os demais: falhou"
}

// --- fetch --------------------------------------------------------------------------

// packageFetch baixa, confere e registra o instalador de cada regra. Um programa com erro
// não impede os outros; o código de saída diz se algum falhou.
func packageFetch(ctx context.Context, c *winget.Client, f *pkgcache.Fetcher, st packageStore, manifest thirdparty.Manifest,
	dir string, args []string, stdout, stderr io.Writer) int {
	o, ok := parsePackageFlags("package fetch", args, false, stderr)
	if !ok {
		return exitConfig
	}
	rules := rulesFor(manifest, o, stderr)
	if len(rules) == 0 {
		return exitConfig
	}
	failed := false
	for i, a := range rules {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		if !fetchOne(ctx, c, f, st, a, o, dir, stdout) {
			failed = true
		}
	}
	if failed {
		return exitRuntime
	}
	return exitOK
}

func fetchOne(ctx context.Context, c *winget.Client, f *pkgcache.Fetcher, st packageStore, a thirdparty.App, o packageFlags,
	dir string, out io.Writer) bool {
	m, err := resolve(ctx, c, a, o.ver)
	if err != nil {
		fmt.Fprintf(out, "%s (%s) ← winget %s\n  ERRO        %v\n", a.Name, a.ID, a.Install.Winget, err)
		return false
	}
	in, _, err := m.Select(wantFor(a, o))
	if err != nil {
		fmt.Fprintf(out, "%s (%s) ← winget %s %s\n  ERRO        %v (veja package show)\n", a.Name, a.ID, m.PackageIdentifier, m.PackageVersion, err)
		return false
	}
	fmt.Fprintf(out, "%s (%s) ← winget %s %s · %s · %s\n", a.Name, a.ID, m.PackageIdentifier, m.PackageVersion, in.Type, in.Architecture)
	r, err := f.Fetch(ctx, dir, in.URL, in.SHA256, in.UsesMsiexec(), func(line string) { fmt.Fprintf(out, "              %s\n", line) })
	var hm *pkgcache.HashMismatchError
	switch {
	case errors.As(err, &hm):
		fmt.Fprintf(out, "  RECUSADO    %v\n", err)
		if !urlHasVersion(in.URL, m.PackageVersion) {
			fmt.Fprintf(out, "              o endereço não tem a versão (%s): o fabricante já publicou outra build e o\n"+
				"              manifesto do winget-pkgs ainda não acompanhou. Nada foi guardado; tente de novo\n"+
				"              quando o winget-pkgs publicar a versão nova.\n", m.PackageVersion)
		} else {
			fmt.Fprintf(out, "              o endereço tem a versão %s: um arquivo diferente num endereço versionado é\n"+
				"              suspeito (troca no fabricante, espelho, interceptação). Nada foi guardado; confira\n"+
				"              antes de tentar de novo.\n", m.PackageVersion)
		}
		return false
	case err != nil:
		fmt.Fprintf(out, "  ERRO        %v\n", err)
		return false
	}
	name := filepath.Join("pacotes", filepath.Base(r.Path))
	if r.Cached {
		fmt.Fprintf(out, "  no cache    %s (%s), conferido\n", name, pkgcache.Bytes(r.Size))
	} else {
		fmt.Fprintf(out, "  baixado     %s (%s) em %s\n", name, pkgcache.Bytes(r.Size), r.Duration.Round(100*time.Millisecond))
	}
	created, err := st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: a.ID, WingetID: m.PackageIdentifier,
		Version: m.PackageVersion, Architecture: in.Architecture, Type: in.Type, Scope: in.Scope, URL: in.URL,
		SHA256: in.SHA256, Size: r.Size, Arguments: in.Arguments(), ReturnCodes: in.ReturnCodes})
	var pc *store.PackageChangedError
	switch {
	case errors.As(err, &pc):
		fmt.Fprintf(out, "  RECUSADO    %v\n", err)
		return false
	case err != nil:
		fmt.Fprintf(out, "  ERRO        registrar: %v\n", err)
		return false
	case created:
		fmt.Fprintln(out, "  registrado  novo")
	default:
		fmt.Fprintln(out, "  registrado  já estava")
	}
	return true
}

// urlHasVersion: o endereço traz a versão, com ou sem os pontos (7z2604 para 26.04).
func urlHasVersion(u, v string) bool {
	return strings.Contains(u, v) || strings.Contains(u, strings.ReplaceAll(v, ".", ""))
}

// --- list ---------------------------------------------------------------------------

func packageList(ctx context.Context, st packageStore, dir string, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("package list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	verify := fs.Bool("verify", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return exitConfig
	}
	list, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "nenhum instalador guardado (use package fetch)")
		return exitOK
	}
	bad := false
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROGRAMA\tVERSÃO\tTIPO\tARQ\tTAMANHO\tSHA-256\tBAIXADO HÁ\tARQUIVO")
	for _, p := range list {
		state := fileState(dir, p, *verify)
		if state != "presente" && state != "conferido" {
			bad = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.AppID, p.Version, p.Type, p.Architecture, pkgcache.Bytes(p.Size),
			p.SHA256[:12], sinceLabel(now.Sub(p.FetchedAt)), state)
	}
	tw.Flush()
	if bad {
		return exitRuntime
	}
	return exitOK
}

// fileState: presente (o tamanho bate), conferido (-verify: o SHA-256 bate), ou o problema.
func fileState(dir string, p store.ThirdPartyPackage, verify bool) string {
	path := filepath.Join(dir, pkgcache.FileName(p.SHA256, p.MSI()))
	if verify {
		if _, err := pkgcache.Check(dir, p.SHA256, p.MSI()); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "SUMIU"
			}
			return "DIFERENTE: " + err.Error()
		}
		return "conferido"
	}
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "SUMIU"
	case err != nil:
		return "ERRO: " + err.Error()
	case st.Size() != p.Size:
		return fmt.Sprintf("TAMANHO %s", pkgcache.Bytes(st.Size()))
	}
	return "presente"
}

// sinceLabel: há quanto tempo, curto (o ago do catálogo só conta horas e dias).
func sinceLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d dias", int(d.Hours()/24))
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
