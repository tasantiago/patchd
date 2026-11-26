package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
	"github.com/tasantiago/patchd/internal/window"
)

const buildingUsage = `uso: patchd-server building <comando> [opções]

comandos:
  list     os prédios do patrimônio (ID estável), os equipamentos, os andares identificados,
           a janela de manutenção e quando ela abre; e os sem localização identificada
  window   grava a mesma janela num ou mais prédios: -id ID[,ID...] -days seg-sex -start 22:00
           -end 04:00 [-tz America/Porto_Velho]; -clear remove; -disable/-enable desativa ou
           reativa sem apagar

A janela de um prédio vale para todos os andares dele. Término antes do início atravessa a
meia-noite (22:00 a 04:00). O fuso padrão é PATCHD_TIMEZONE. Os jobs já criados mantêm a
janela deles; uma campanha "na janela" que chegar a um prédio sem janela ativa pausa.
`

type buildingStore interface {
	api.Store
}

func runBuilding(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, buildingUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os prédios exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
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
	tz, _ := look("PATCHD_TIMEZONE")
	return buildingCommand(ctx, store.NewPostgres(pool), args, tz, stdout, stderr)
}

func buildingCommand(ctx context.Context, st buildingStore, args []string, defaultTZ string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("building "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "list":
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		list, unlocated, err := api.Buildings(ctx, st, nil)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tPRÉDIO\tEQUIP.\tSEM ANDAR\tCONFLITO\tANDARES\tJANELA\tPRÓXIMA")
		for _, b := range list {
			name := texto.PlaceName(b.Name)
			if !b.Present {
				name += " (fora do patrimônio atual)"
			}
			win := orDash(b.Window)
			if b.Window != "" && !b.WindowOn {
				win += " (desativada)"
			}
			fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n", b.ID, name, b.Machines, b.Unfloored, b.Conflicts, orDash(strings.Join(b.Floors, ", ")), win, orDash(b.Next))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d prédio(s); %d equipamento(s) sem localização identificada\n", len(list), unlocated)
		return exitOK
	case "window":
		idList := fs.String("id", "", "IDs dos prédios, separados por vírgula (building list)")
		days := fs.String("days", "", "dias: seg-sex, sab,dom, todos")
		start := fs.String("start", "", "início, HH:MM no fuso -tz")
		end := fs.String("end", "", "término, HH:MM (antes do início: no dia seguinte)")
		tz := fs.String("tz", defaultTZ, "fuso horário IANA (env PATCHD_TIMEZONE)")
		clear := fs.Bool("clear", false, "remove a janela")
		disable := fs.Bool("disable", false, "desativa a janela sem apagar")
		enable := fs.Bool("enable", false, "reativa a janela desativada")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		var ids []int64
		for _, x := range strings.Split(*idList, ",") {
			if x = strings.TrimSpace(x); x == "" {
				continue
			}
			id, err := strconv.ParseInt(x, 10, 64)
			if err != nil || id <= 0 {
				fmt.Fprintf(stderr, "patchd-server: -id: %q não é um ID de prédio\n", x)
				return exitConfig
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 || len(ids) > store.MaxWindowBatch {
			fmt.Fprintln(stderr, "patchd-server: informe -id (os IDs de building list)")
			return exitConfig
		}
		switch {
		case *clear:
			n, err := st.DeleteBuildingWindows(ctx, ids)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			fmt.Fprintf(stderr, "%d janela(s) removida(s); os jobs já criados mantêm a janela deles\n", n)
			return exitOK
		case *disable || *enable:
			n, err := st.SetBuildingWindowsEnabled(ctx, ids, *enable, "linha de comando")
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			fmt.Fprintf(stderr, "%d janela(s) %s\n", n, map[bool]string{true: "ativada(s)", false: "desativada(s)"}[*enable])
			return exitOK
		}
		w, err := parseBuildingWindow(*days, *start, *end, *tz)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		if err := st.SetBuildingWindows(ctx, ids, w, "linha de comando"); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stderr, "janela %s gravada em %d prédio(s)\n", w, len(ids))
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], buildingUsage)
		return exitConfig
	}
}

// parseBuildingWindow: dias, início e término (o término antes do início passa da meia-noite).
func parseBuildingWindow(days, start, end, tz string) (window.Window, error) {
	if end == "" {
		return window.Window{}, fmt.Errorf("informe -days, -start e -end (ou -clear, -disable, -enable)")
	}
	s, err := window.ParseStart(start)
	if err != nil {
		return window.Window{}, err
	}
	e, err := window.ParseStart(end)
	if err != nil {
		return window.Window{}, fmt.Errorf("término: %w", err)
	}
	dur := e - s
	if dur < 0 {
		dur += 24 * time.Hour
	}
	if dur == 0 {
		return window.Window{}, fmt.Errorf("o término é igual ao início")
	}
	return parseWindow(days, start, dur, tz)
}
