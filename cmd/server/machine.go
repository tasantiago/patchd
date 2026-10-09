package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

const machineUsage = `uso: patchd-server machine <comando> [opções]

comandos:
  list     lista as máquinas da frota, com os alertas de identidade que ligam umas às
           outras (mesma instalação, mesmo hardware); -retired lista as aposentadas
  retire   aposenta: -id ID -reason "motivo". A máquina sai da lista, do compliance e do
           painel, e a credencial dela deixa de valer (o agente recebe 401). Nada é apagado.
  restore  devolve à frota: -id ID. A credencial volta a valer.

ID pode ser o começo do ID, desde que case com uma só máquina. -sem-nome, no list, omite
o nome das máquinas (para colar a saída num chamado ou no chat).
A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
`

// machineStore é o que os comandos de máquina usam do banco.
type machineStore interface {
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	RetiredMachines(ctx context.Context) ([]store.RetiredMachine, error)
	IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	RestoreMachine(ctx context.Context, id string) (bool, error)
}

func runMachine(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, machineUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os comandos de máquina exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	return machineCommand(ctx, store.NewPostgres(pool), args, time.Now().UTC(), stdout, stderr)
}

func machineCommand(ctx context.Context, st machineStore, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("machine "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "list":
		retired := fs.Bool("retired", false, "lista as aposentadas")
		noName := fs.Bool("sem-nome", false, "omite o nome das máquinas")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if err := machineList(ctx, st, *retired, *noName, now, stdout); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		return exitOK
	case "retire", "restore":
		prefix := fs.String("id", "", "ID (ou começo do ID) da máquina")
		var reason *string
		if args[0] == "retire" {
			reason = fs.String("reason", "", "por que a máquina sai da frota (fica registrado)")
		}
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if *prefix == "" {
			fmt.Fprintln(stderr, "patchd-server: informe -id")
			return exitConfig
		}
		if reason != nil && len(strings.TrimSpace(*reason)) < 3 {
			fmt.Fprintln(stderr, "patchd-server: informe -reason (ex.: \"registro antigo da VM reinstalada\")")
			return exitConfig
		}
		id, ok, err := st.ResolveMachine(ctx, *prefix)
		if errors.Is(err, store.ErrMachineAmbiguous) {
			fmt.Fprintf(stderr, "patchd-server: %q casa com mais de uma máquina; use mais caracteres do ID\n", *prefix)
			return exitConfig
		}
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if !ok {
			fmt.Fprintf(stderr, "patchd-server: nenhuma máquina com o ID %q\n", *prefix)
			return exitRuntime
		}
		var changed bool
		if reason != nil {
			changed, err = st.RetireMachine(ctx, id, strings.TrimSpace(*reason), "linha de comando")
		} else {
			changed, err = st.RestoreMachine(ctx, id)
		}
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		case !changed && reason != nil:
			fmt.Fprintf(stderr, "a máquina %s já estava aposentada\n", id)
		case !changed:
			fmt.Fprintf(stderr, "a máquina %s não estava aposentada\n", id)
		case reason != nil:
			fmt.Fprintf(stderr, "máquina %s aposentada: saiu da frota, e a credencial dela deixou de valer (restore desfaz)\n", id)
		default:
			fmt.Fprintf(stderr, "máquina %s de volta à frota; a credencial voltou a valer\n", id)
		}
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], machineUsage)
		return exitConfig
	}
}

// relationLabel traduz a relação do alerta de identidade.
func relationLabel(r string) string {
	switch r {
	case "reenrollment":
		return "mesma instalação"
	case "clone":
		return "clone"
	case "same_hardware":
		return "mesmo hardware"
	}
	return r
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// machineList mostra as máquinas e, para cada uma, as outras ligadas a ela pelos alertas
// de identidade (nos dois sentidos), marcando as que já estão aposentadas.
func machineList(ctx context.Context, st machineStore, retired, noName bool, now time.Time, out io.Writer) error {
	active, err := st.Machines(ctx)
	if err != nil {
		return err
	}
	old, err := st.RetiredMachines(ctx)
	if err != nil {
		return err
	}
	links, err := st.IdentityLinks(ctx)
	if err != nil {
		return err
	}
	isRetired := map[string]bool{}
	for _, r := range old {
		isRetired[r.ID] = true
	}
	related := map[string][]string{}
	for _, l := range links {
		for _, pair := range [][2]string{{l.MachineID, l.RelatedMachineID}, {l.RelatedMachineID, l.MachineID}} {
			other := short(pair[1])
			if isRetired[pair[1]] {
				other += " (aposentada)"
			}
			related[pair[0]] = append(related[pair[0]], relationLabel(l.Relation)+": "+other)
		}
	}
	for id := range related {
		sort.Strings(related[id])
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := []string{"MÁQUINA", "ANEL", "NOME", "SO", "ÚLTIMO CONTATO", "INVENTÁRIO"}
	if noName {
		header = append(header[:2], header[3:]...)
	}
	if retired {
		header = append(header, "APOSENTADA (UTC)", "POR", "MOTIVO")
	}
	header = append(header, "LIGADA A")
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	row := func(m protocol.MachineSummary, extra ...string) {
		inv := "não"
		if m.InventoryHash != "" {
			inv = "sim"
		}
		cols := []string{short(m.ID), strconv.Itoa(m.Ring), orDash(m.Hostname), orDash(strings.TrimSpace(m.OSName + " " + m.OSVersion)), since(now.Sub(m.LastSeenAt)), inv}
		if noName {
			cols = append(cols[:2], cols[3:]...)
		}
		cols = append(cols, extra...)
		cols = append(cols, orDash(strings.Join(related[m.ID], "; ")))
		fmt.Fprintln(tw, strings.Join(cols, "\t"))
	}
	if retired {
		for _, r := range old {
			row(r.MachineSummary, r.RetiredAt.Format("2006-01-02 15:04"), orDash(r.RetiredBy), r.Reason)
		}
	} else {
		for _, m := range active {
			row(m)
		}
	}
	_ = tw.Flush()
	n := len(active)
	if retired {
		n = len(old)
	}
	fmt.Fprintf(out, "%d máquina(s)", n)
	if !retired && len(old) > 0 {
		fmt.Fprintf(out, "; %d aposentada(s) (machine list -retired)", len(old))
	}
	fmt.Fprintln(out)
	return nil
}
