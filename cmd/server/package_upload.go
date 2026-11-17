package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpupload"
)

// packageUpload: package upload -app ID -version V -file ARQUIVO [-arch x64] [-args "..."].
// O mesmo envio do painel (internal/tpupload), para o instalador próprio grande ou para quem
// prefere o terminal do servidor. Não aprova nem substitui: isso fica no painel ou no approve.
func packageUpload(ctx context.Context, st tpupload.Store, manifest thirdparty.Manifest, dir string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("package upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	app := fs.String("app", "", "o programa (instalador próprio incluído pelo painel)")
	ver := fs.String("version", "", "a versão do instalador, como em Programas e Recursos")
	file := fs.String("file", "", "o arquivo .msi ou .exe")
	arch := fs.String("arch", "x64", "x64, x86, arm64 ou neutral")
	line := fs.String("args", "", "os argumentos da instalação silenciosa (vazio: o padrão do tipo)")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if *app == "" || *ver == "" || *file == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "patchd-server: package upload -app ID -version V -file ARQUIVO [-arch x64] [-args \"...\"]")
		return exitConfig
	}
	var rule *thirdparty.App
	for _, a := range manifest.For("windows") {
		if a.ID == *app {
			rule = &a
		}
	}
	if rule == nil || !rule.Uploaded() {
		fmt.Fprintf(stderr, "patchd-server: %q não é um instalador próprio (inclua pelo painel: Patch › Programas de terceiros › Incluir programa › Instalador próprio)\n", *app)
		return exitConfig
	}
	in := tpupload.Input{Version: *ver, Arch: *arch, Args: *line, FileName: *file}
	if err := tpupload.Check(*rule, in); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	f, err := os.Open(*file) // #nosec G304 -- o arquivo que o admin escolheu no terminal do servidor
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	defer f.Close()
	pk, err := tpupload.Save(ctx, st, dir, *rule, in, f)
	var pr *tpupload.Problem
	switch {
	case errors.As(err, &pr):
		fmt.Fprintf(stderr, "patchd-server: %s\n", pr.Msg)
		return exitConfig
	case err != nil:
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "%s %s guardado · %s · %s · %s · sha256 %s · argumentos %q\n", pk.AppID, pk.Version, pk.Type, pk.Architecture,
		pkgcache.Bytes(pk.Size), pk.SHA256, pk.Arguments)
	fmt.Fprintf(stdout, "próximo passo: aprove a versão (painel, ou package approve -app %s -version %s)\n", pk.AppID, pk.Version)
	return exitOK
}
