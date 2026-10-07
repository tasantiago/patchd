package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/wintrust"
)

// newOfflineCab monta o catálogo offline distribuído pelo servidor (Aula 6.6). nil fora do
// Windows (o wsusscn2.cab só serve ao Windows Update) e quando -offline-cab fixa um
// arquivo: o caminho informado pelo administrador vale mais que o anúncio.
func newOfflineCab(logger *slog.Logger, client *agent.Client, opts loopOptions) *agent.OfflineCab {
	if !wintrust.Supported {
		return nil
	}
	if opts.OfflineCatalog != "" {
		logger.Info("catálogo offline fixo por -offline-cab/PATCHD_OFFLINE_CAB; o anunciado pelo servidor é ignorado",
			"offline_cab", opts.OfflineCatalog)
		return nil
	}
	return &agent.OfflineCab{Client: client, DataDir: opts.DataDir, Verify: verifySigner, Logger: logger}
}

// verifySigner confere a assinatura da Microsoft e devolve a cadeia (também na recusa).
func verifySigner(path string) (string, error) {
	sig, err := wintrust.Verify(path)
	return sig.String(), err
}

// localOfflineCab é o catálogo baixado pelo serviço, para o "-scan" no terminal; "" se não há.
func localOfflineCab(dataDir string) string {
	if !wintrust.Supported {
		return ""
	}
	return (&agent.OfflineCab{DataDir: dataDir}).Path()
}

// runVerifyCab é o "patchd-agent verify-cab [arquivo]": confere a assinatura Authenticode de
// um arquivo, como o agente faz antes de usar o catálogo recebido do servidor. Sem arquivo,
// mostra o catálogo baixado pelo serviço e confere de novo.
func runVerifyCab(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-cab", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", config.String(look, "PATCHD_DATA_DIR", platform.DataDir()), "pasta de dados do agente (env PATCHD_DATA_DIR)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "uso: patchd-agent verify-cab [-data-dir PASTA] [ARQUIVO]")
		fmt.Fprintln(stderr, "  sem ARQUIVO: o catálogo offline baixado pelo serviço")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitConfig
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return exitConfig
	}
	if !wintrust.Supported {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", wintrust.ErrUnsupported)
		return exitConfig
	}

	path := fs.Arg(0)
	if path == "" {
		cab := &agent.OfflineCab{DataDir: *dataDir}
		st := cab.State()
		if st.Rejected != "" {
			fmt.Fprintf(stdout, "último recusado: %s... em %s: %s\n", st.Rejected[:16], st.RejectedAt.Local().Format(time.DateTime), st.RejectedError)
		}
		if path = cab.Path(); path == "" {
			fmt.Fprintln(stdout, "nenhum catálogo offline baixado do servidor ainda")
			return exitRuntime
		}
		fmt.Fprintf(stdout, "em uso:       publicado pela Microsoft em %s UTC, conferido em %s\n",
			st.PublishedAt.UTC().Format("2006-01-02 15:04"), st.VerifiedAt.Local().Format(time.DateTime))
	}

	fi, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "arquivo:      %s (%d bytes)\n", path, fi.Size())
	start := time.Now()
	sig, err := wintrust.Verify(path)
	took := time.Since(start).Round(time.Millisecond)
	for i, c := range sig.Chain {
		label := "  cadeia:     "
		if i == 0 {
			label = "assinou:      "
		}
		fmt.Fprintf(stdout, "%s%s (O=%s) sha1 %s\n", label, c.CommonName, c.Organization, c.SHA1)
	}
	if err != nil {
		fmt.Fprintf(stdout, "resultado:    RECUSADO em %s: %v\n", took, err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "resultado:    OK em %s: assinado pela Microsoft\n", took)
	return exitOK
}
