package tpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tasantiago/patchd/internal/choco"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// O instalador pelo pacote do Chocolatey (store.InstallerSource com Kind chocolatey): o
// servidor baixa o .nupkg da comunidade (a versão pedida, ou a mais nova), lê do script o link
// e o SHA-256 que ele baixaria, sem executar nada, e baixa o instalador do link (o site do
// fabricante), conferido pelo SHA-256 do pacote. A versão é a do pacote (sem a data da
// correção). O tipo e os argumentos são os da fonte: o script do Chocolatey não é usado.

// CheckChoco confere a fonte do Chocolatey antes de gravar.
func CheckChoco(s store.InstallerSource, rule thirdparty.App) error {
	if rule.Install == nil || rule.Uploaded() || rule.OS != "windows" {
		return errors.New("o Chocolatey vale para os programas do Windows com instalação pelo winget")
	}
	if !choco.ValidID(s.PackageID) {
		return errors.New("o pacote do Chocolatey é o id da página do pacote (ex.: googlechrome, adobereader, firefox)")
	}
	for _, v := range []string{s.URLVar, s.ChecksumVar} {
		if v != "" && !chocoVar.MatchString(v) {
			return fmt.Errorf("variável %q: o nome como está no script (ex.: $MUIurl64 ou url64bit)", v)
		}
	}
	if !winget.Supported(s.Type) {
		return fmt.Errorf("tipo %q: use msi, wix, nullsoft, inno, burn ou exe", s.Type)
	}
	if s.Type == "exe" && strings.TrimSpace(s.Arguments) == "" {
		return errors.New("o exe genérico exige os argumentos da instalação silenciosa (o silentArgs do script do pacote)")
	}
	if _, err := jobs.SplitArgs(s.Type, s.Arguments); err != nil {
		return fmt.Errorf("argumentos: %w", err)
	}
	return nil
}

// Choco baixa, confere e registra o instalador pelo pacote do Chocolatey.
func Choco(ctx context.Context, hc *http.Client, base string, f *pkgcache.Fetcher, st Store, a thirdparty.App, src store.InstallerSource,
	version, dir string, out io.Writer) bool {
	fail := func(format string, args ...any) bool {
		fmt.Fprintf(out, "  ERRO        "+format+"\n", args...)
		return false
	}
	pedida := version
	if pedida == "" {
		pedida = "a mais nova"
	}
	fmt.Fprintf(out, "%s (%s) ← Chocolatey %s · %s · %s\n", a.Name, a.ID, src.PackageID, pedida, src.Type)
	if err := CheckChoco(src, a); err != nil {
		return fail("%v", err)
	}
	pkg, err := choco.Fetch(ctx, hc, base, src.PackageID, version, f.UserAgent)
	if err != nil {
		return fail("pacote: %v", err)
	}
	in, err := choco.Extract(pkg.Script, src.URLVar, src.ChecksumVar)
	if err != nil {
		if len(pkg.Embedded) > 0 {
			return fail("o pacote %s %s traz o instalador dentro (%s): o patchd só usa o link do fabricante; use a fonte do site do fabricante ou o instalador próprio",
				src.PackageID, pkg.Version, strings.Join(pkg.Embedded, ", "))
		}
		return fail("%v", err)
	}
	ver := choco.ProgramVersion(pkg.Version)
	fmt.Fprintf(out, "  pacote      %s %s → versão %s\n  link        %s (de %s)\n", src.PackageID, pkg.Version, ver, in.URL, in.URLVar)
	if in.SHA256 != "" {
		fmt.Fprintf(out, "  SHA-256     %s… (do pacote)\n", in.SHA256[:12])
	}
	return keep(ctx, f, st, a, in.URL, in.SHA256, src.Type, src.Arguments, ver, dir, out)
}
