package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

const packageUsage = `uso: patchd-server package show (-app ID | -all | -file MANIFESTO) [opções]

Mostra o instalador que o patchd usaria para atualizar um programa de terceiros no
Windows (Aula 14.1): o pacote do winget-pkgs da regra, a versão, o instalador escolhido
para a arquitetura, de onde baixar, o SHA-256 esperado e a linha de comando silenciosa.
Nada é baixado nem executado.

  -app ID        a regra do manifesto de terceiros (ex.: 7zip, google-chrome)
  -all           todas as regras do Windows com "install"
  -file ARQUIVO  um manifesto de instalador local (.installer.yaml), sem rede
  -version V     a versão no winget-pkgs (padrão: a mais nova; pula a API do GitHub)
  -arch A        x64 (padrão), x86 ou arm64
  -type T        exige um tipo de instalador, no lugar do da regra

O manifesto de terceiros é o embutido, mais PATCHD_THIRDPARTY_MANIFEST se definido.
`

func runPackage(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprint(stderr, packageUsage)
		return exitConfig
	}
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	manifest, err := thirdparty.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	c := winget.NewClient("patchd-server/" + buildinfo.Get().Version)
	c.HTTP = retry.Wrap(c.HTTP, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return packageShow(ctx, c, manifest, args[1:], stdout, stderr)
}

func packageShow(ctx context.Context, c *winget.Client, manifest thirdparty.Manifest, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("package show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	app := fs.String("app", "", "")
	all := fs.Bool("all", false, "")
	file := fs.String("file", "", "")
	ver := fs.String("version", "", "")
	arch := fs.String("arch", "x64", "")
	typ := fs.String("type", "", "")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if n := btoi(*app != "") + btoi(*all) + btoi(*file != ""); n != 1 || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "patchd-server: use um (e só um) de -app, -all e -file")
		return exitConfig
	}
	if *typ != "" && !winget.Supported(*typ) {
		fmt.Fprintf(stderr, "patchd-server: -type %q: use msi, wix, nullsoft, inno, burn ou exe\n", *typ)
		return exitConfig
	}
	if *all && *ver != "" {
		fmt.Fprintln(stderr, "patchd-server: -version vale para um programa só (-app)")
		return exitConfig
	}

	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		m, err := winget.ParseInstaller(data)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %s: %v\n", *file, err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%s %s (arquivo local)\n", m.PackageIdentifier, m.PackageVersion)
		if !printSelection(m, winget.Want{Arch: *arch, Type: *typ}, stdout) {
			return exitRuntime
		}
		return exitOK
	}

	var rules []thirdparty.App
	for _, a := range manifest.For("windows") {
		if a.Install == nil {
			continue
		}
		if *all || a.ID == *app {
			rules = append(rules, a)
		}
	}
	if len(rules) == 0 {
		if *all {
			fmt.Fprintln(stderr, "patchd-server: nenhuma regra do Windows com \"install\" no manifesto")
		} else {
			fmt.Fprintf(stderr, "patchd-server: a regra %q do Windows não existe ou não tem \"install\" (veja catalog terceiros)\n", *app)
		}
		return exitConfig
	}
	failed := false
	for i, a := range rules {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		want := winget.Want{Arch: *arch, Type: a.Install.Type}
		if *typ != "" {
			want.Type = *typ
		}
		v := *ver
		if v == "" {
			versions, err := c.Versions(ctx, a.Install.Winget)
			if err != nil {
				fmt.Fprintf(stdout, "%s (%s) ← winget %s\n  ERRO: %v\n", a.Name, a.ID, a.Install.Winget, err)
				failed = true
				continue
			}
			v = versions[0]
		}
		m, err := c.Installer(ctx, a.Install.Winget, v)
		if err != nil {
			fmt.Fprintf(stdout, "%s (%s) ← winget %s %s\n  ERRO: %v\n", a.Name, a.ID, a.Install.Winget, v, err)
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s (%s) ← winget %s %s\n", a.Name, a.ID, m.PackageIdentifier, m.PackageVersion)
		if !printSelection(m, want, stdout) {
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

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
