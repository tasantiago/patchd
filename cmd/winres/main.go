// Comando winres: grava o recurso de versão de um executável do Windows (Aula 20.9) como .syso,
// que o go build junta ao executável. O scripts/build.sh roda antes de compilar o agente e a
// Central:
//
//	go run ./cmd/winres -version v0.17.14 -name "Agente patchd" -out cmd/agent
//	go run ./cmd/winres -version v0.17.14 -name "Central de software" -file patchd-app.exe -out cmd/app
//
// -name é a "Descrição do arquivo": o que o Windows mostra no Gerenciador de Tarefas, nos
// aplicativos de inicialização e no título dos avisos da Central.
//
// Grava PASTA/versao_windows_amd64.syso e versao_windows_arm64.syso (fora do git).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/winres"
)

// exeName: o nome do arquivo, sem pasta.
var exeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,60}\.exe$`)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("winres", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "versão do patchd")
	name := fs.String("name", protocol.DefaultAppName, "a descrição do arquivo")
	file := fs.String("file", "patchd-agent.exe", "o nome original do arquivo")
	company := fs.String("company", "", "a organização (opcional)")
	out := fs.String("out", "cmd/agent", "pasta do pacote do agente")
	if err := fs.Parse(args); err != nil || *version == "" || fs.NArg() > 0 || !exeName.MatchString(*file) {
		fmt.Fprintln(stderr, "uso: winres -version V [-name TEXTO] [-file NOME.exe] [-company TEXTO] [-out PASTA]")
		return 2
	}
	vi, err := winres.VersionInfo(winres.Info{Version: *version, FileDescription: *name, ProductName: "patchd",
		CompanyName: *company, OriginalFilename: *file, InternalName: strings.TrimSuffix(*file, ".exe")})
	if err != nil {
		fmt.Fprintf(stderr, "winres: %v\n", err)
		return 2
	}
	for _, arch := range []string{"amd64", "arm64"} {
		obj, err := winres.Syso(arch, vi)
		if err != nil {
			fmt.Fprintf(stderr, "winres: %v\n", err)
			return 1
		}
		dest := filepath.Join(*out, "versao_windows_"+arch+".syso")
		// #nosec G306 -- objeto de build, sem segredo
		if err := os.WriteFile(dest, obj, 0o644); err != nil {
			fmt.Fprintf(stderr, "winres: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "ok  %s\n", dest)
	}
	return 0
}
