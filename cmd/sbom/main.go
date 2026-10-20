// Comando sbom: a lista de componentes (CycloneDX 1.5) de cada executável do patchd, para o
// pacote de revisão externa (Aula 20.8, S9; scripts/revisao.sh).
//
//	go run ./cmd/sbom -out PASTA [-version v0.17.11] [-revision COMMIT] EXECUTÁVEL...
//
// Grava PASTA/<executável>.cdx.json e PASTA/modulos.txt (todos os módulos, com os
// executáveis que usam cada um). Recusa executável que não é Go, compilado com alterações
// não commitadas, ou (com -revision) de outro commit.
package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/tasantiago/patchd/internal/sbom"
)

const (
	exitOK     = 0
	exitConfig = 2
	exitFail   = 1
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "pasta de saída")
	version := fs.String("version", "", "versão do patchd (a do build)")
	revision := fs.String("revision", "", "commit esperado (vcs.revision); vazio: não confere")
	if err := fs.Parse(args); err != nil || *out == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, "uso: sbom -out PASTA [-version V] [-revision COMMIT] EXECUTÁVEL...")
		return exitConfig
	}
	// #nosec G301 -- o SBOM é público: vai para quem revisa
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "sbom: %v\n", err)
		return exitFail
	}
	var boms []sbom.BOM
	for _, path := range fs.Args() {
		b, err := one(path, *version, *revision)
		if err != nil {
			fmt.Fprintf(stderr, "sbom: %s: %v\n", filepath.Base(path), err)
			return exitFail
		}
		data, _ := json.MarshalIndent(b, "", "  ")
		dest := filepath.Join(*out, filepath.Base(path)+".cdx.json")
		// #nosec G306 -- o SBOM é público: vai para quem revisa
		if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "sbom: %v\n", err)
			return exitFail
		}
		boms = append(boms, b)
		fmt.Fprintf(stdout, "ok  %s (%d componentes, Go %s)\n", filepath.Base(dest), len(b.Components), b.Components[0].Version)
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MÓDULO\tVERSÃO\tEXECUTÁVEIS")
	for _, m := range sbom.Modules(boms) {
		in := strings.Join(m.In, ", ")
		if len(m.In) == len(boms) {
			in = "todos"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Name, m.Version, in)
	}
	_ = tw.Flush()
	// #nosec G306 -- o resumo é público: vai para quem revisa
	if err := os.WriteFile(filepath.Join(*out, "modulos.txt"), buf.Bytes(), 0o644); err != nil {
		fmt.Fprintf(stderr, "sbom: %v\n", err)
		return exitFail
	}
	return exitOK
}

func one(path, version, revision string) (sbom.BOM, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sbom.BOM{}, err
	}
	bi, err := buildinfo.Read(bytes.NewReader(data))
	if err != nil {
		return sbom.BOM{}, fmt.Errorf("não é um executável Go com informação de build: %v", err)
	}
	if revision != "" {
		got := ""
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				got = s.Value
			}
		}
		if got == "" || !strings.HasPrefix(got, revision) && !strings.HasPrefix(revision, got) {
			return sbom.BOM{}, fmt.Errorf("é do commit %q, não do %s (o dist/ é de outro build)", short(got), short(revision))
		}
	}
	b, err := sbom.Build(sbom.Artifact{Name: filepath.Base(path), Version: version, SHA256: sbom.Sum(data)}, bi, version)
	if errors.Is(err, sbom.ErrModified) {
		return b, fmt.Errorf("%w: refaça o build com a árvore limpa", err)
	}
	return b, err
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
