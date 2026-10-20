// Comando winres: grava o recurso de versão do agente do Windows (Aula 20.9) como .syso,
// que o go build junta ao executável. O scripts/build.sh roda antes de compilar o agente:
//
//	go run ./cmd/winres -version v0.17.13 -name "Central de software" -out cmd/agent
//
// Grava cmd/agent/versao_windows_amd64.syso e versao_windows_arm64.syso (fora do git).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tasantiago/patchd/internal/winres"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("winres", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "versão do patchd")
	name := fs.String("name", "Central de software", "a descrição que o usuário vê (avisos, Gerenciador de Tarefas)")
	company := fs.String("company", "", "a organização (opcional)")
	out := fs.String("out", "cmd/agent", "pasta do pacote do agente")
	if err := fs.Parse(args); err != nil || *version == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "uso: winres -version V [-name TEXTO] [-company TEXTO] [-out PASTA]")
		return 2
	}
	vi, err := winres.VersionInfo(winres.Info{Version: *version, FileDescription: *name, ProductName: "patchd",
		CompanyName: *company, OriginalFilename: "patchd-agent.exe", InternalName: "patchd-agent"})
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
