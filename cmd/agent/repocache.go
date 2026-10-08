package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/repoconfig"
)

// newRepoConfig: só no Linux o agente configura o apt e o dnf (Aula 6.6, parte 4d).
func newRepoConfig() *repoconfig.Config {
	if runtime.GOOS != "linux" {
		return nil
	}
	return &repoconfig.Config{}
}

const repoCacheUsage = `uso: patchd-agent repo-cache <comando>

comandos:
  show                         mostra os gerenciadores encontrados e os arquivos gravados pelo patchd
  apply -url URL [-apt-hosts]  grava a configuração à mão (o serviço faz isso sozinho a cada check-in)
  remove                       apaga a configuração gravada pelo patchd
`

// runRepoCache é o "patchd-agent repo-cache ...". Com o serviço rodando, quem manda é o
// anúncio do servidor: um apply à mão vale até o próximo check-in.
func runRepoCache(args []string, stdout, stderr io.Writer) int {
	rc := newRepoConfig()
	if rc == nil {
		fmt.Fprintln(stderr, "patchd-agent: o cache de repositórios só é configurado no Linux")
		return exitConfig
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, repoCacheUsage)
		return exitConfig
	}
	switch args[0] {
	case "show":
		fmt.Fprintf(stdout, "gerenciadores: %s\n", orNone(rc.Managers()))
		files := rc.Installed()
		if len(files) == 0 {
			fmt.Fprintln(stdout, "nenhum arquivo do patchd gravado")
			return exitOK
		}
		names := make([]string, 0, len(files))
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		for _, f := range names {
			fmt.Fprintf(stdout, "== %s\n%s", f, files[f])
		}
		return exitOK
	case "apply":
		fs := flag.NewFlagSet("repo-cache apply", flag.ContinueOnError)
		fs.SetOutput(stderr)
		u := fs.String("url", "", "URL do cache, ex.: http://patchd.exemplo:8080")
		hosts := fs.String("apt-hosts", "archive.ubuntu.com,security.ubuntu.com", "origens do apt que passam pelo cache")
		if err := fs.Parse(args[1:]); err != nil {
			return exitConfig
		}
		adv := protocol.RepoCache{URL: *u, AptHosts: strings.Split(*hosts, ",")}
		res, err := rc.Apply(context.Background(), adv)
		fmt.Fprintln(stdout, res.Summary())
		if err != nil {
			fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
			return exitRuntime
		}
		return exitOK
	case "remove":
		res, err := rc.Remove()
		fmt.Fprintln(stdout, res.Summary())
		if err != nil {
			fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
			return exitRuntime
		}
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-agent: comando desconhecido %q\n\n%s", args[0], repoCacheUsage)
		return exitConfig
	}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "nenhum suportado (apt ou dnf5)"
	}
	return strings.Join(s, ", ")
}
