// Package tpfetch baixa, confere e registra o instalador de um programa de terceiros (Aula
// 14.2): do manifesto do winget-pkgs para a pasta de pacotes do servidor, preso pelo SHA-256.
// É o mesmo caminho para o "patchd-server package fetch" e para o botão do painel (Aula 23.3).
package tpfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// Options escolhe o instalador: a versão no winget-pkgs (vazia: a mais nova), a arquitetura
// (vazia: a do winget.Want, x64) e o tipo (vazio: o da regra).
type Options struct {
	Version, Arch, Type string
}

// Store é o que o fetch grava: o registro do instalador.
type Store interface {
	SaveThirdPartyPackage(ctx context.Context, p store.ThirdPartyPackage) (bool, error)
}

// Resolve lê o manifesto do winget da regra (a versão pedida ou a mais nova).
func Resolve(ctx context.Context, c *winget.Client, a thirdparty.App, ver string) (winget.Manifest, error) {
	if ver == "" {
		versions, err := c.Versions(ctx, a.Install.Winget)
		if err != nil {
			return winget.Manifest{}, err
		}
		ver = versions[0]
	}
	return c.Installer(ctx, a.Install.Winget, ver)
}

// Want: o instalador que a regra pede, com a arquitetura e o tipo das opções.
func Want(a thirdparty.App, o Options) winget.Want {
	w := winget.Want{Arch: o.Arch, Type: a.Install.Type}
	if o.Type != "" {
		w.Type = o.Type
	}
	return w
}

// One baixa, confere e registra o instalador da regra, escrevendo o andamento em out, uma
// linha por etapa. Devolve se deu certo; um erro aparece em out (ERRO ou RECUSADO).
func One(ctx context.Context, c *winget.Client, f *pkgcache.Fetcher, st Store, a thirdparty.App, o Options,
	dir string, out io.Writer) bool {
	m, err := Resolve(ctx, c, a, o.Version)
	if err != nil {
		fmt.Fprintf(out, "%s (%s) ← winget %s\n  ERRO        %v\n", a.Name, a.ID, a.Install.Winget, err)
		return false
	}
	in, _, err := m.Select(Want(a, o))
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
		if !URLHasVersion(in.URL, m.PackageVersion) {
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

// URLHasVersion: o endereço traz a versão, com ou sem os pontos (7z2604 para 26.04).
func URLHasVersion(u, v string) bool {
	return strings.Contains(u, v) || strings.Contains(u, strings.ReplaceAll(v, ".", ""))
}
