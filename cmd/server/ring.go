package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

const ringUsage = `uso: patchd-server ring <comando> [opções]

comandos:
  list     os anéis em uso: quantas máquinas, a janela e quando ela abre de novo
  set      põe máquinas num anel: -machine ID[,ID...] -ring N (0 = piloto, 1 = padrão, até 9)
  window   define a janela de manutenção do anel: -ring N -days seg-sex -start 12:00
           -duration 2h [-tz America/Porto_Velho]; -clear remove a janela

Dias: seg, ter, qua, qui, sex, sab, dom, intervalos (seg-sex) e listas (sab,dom), ou
"todos". O horário é o do fuso -tz (padrão: PATCHD_TIMEZONE). Uma janela que passa da
meia-noite pertence ao dia em que começa (sex 22:00 por 6h termina sábado às 04:00).
Os jobs criados com "job create -window" valem só dentro da próxima janela do anel.
`

// ringStore é o que os comandos de anel usam do banco.
type ringStore interface {
	machineResolver
	SetMachineRing(ctx context.Context, id string, ring int) (bool, error)
	SetWindow(ctx context.Context, ring int, w window.Window, by string) error
	DeleteWindow(ctx context.Context, ring int) (bool, error)
	Windows(ctx context.Context) ([]store.RingWindow, error)
}

func runRing(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, ringUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os anéis exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
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
	return ringCommand(ctx, store.NewPostgres(pool), args, tz, time.Now().UTC(), stdout, stderr)
}

func ringCommand(ctx context.Context, st ringStore, args []string, defaultTZ string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ring "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "list":
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if err := ringList(ctx, st, now, stdout); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		return exitOK
	case "set":
		machines := fs.String("machine", "", "IDs (ou começos de ID), separados por vírgula")
		ring := fs.Int("ring", -1, "anel: 0 = piloto, 1 = padrão, até 9")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if *machines == "" || *ring < 0 || *ring > store.MaxRing {
			fmt.Fprintf(stderr, "patchd-server: informe -machine e -ring (0 a %d)\n", store.MaxRing)
			return exitConfig
		}
		for _, prefix := range strings.Split(*machines, ",") {
			id, code := resolveActive(ctx, st, strings.TrimSpace(prefix), stderr)
			if code != exitOK {
				return code
			}
			ok, err := st.SetMachineRing(ctx, id, *ring)
			if err != nil || !ok {
				fmt.Fprintf(stderr, "patchd-server: a máquina %s não mudou de anel (%v)\n", short(id), err)
				return exitRuntime
			}
			fmt.Fprintf(stderr, "máquina %s no anel %d%s\n", short(id), *ring, pilotLabel(*ring))
		}
		return exitOK
	case "window":
		ring := fs.Int("ring", -1, "anel da janela")
		days := fs.String("days", "", "dias: seg-sex, sab,dom, todos")
		start := fs.String("start", "", "início, HH:MM no fuso -tz")
		dur := fs.Duration("duration", 0, "duração: de 10m a 24h")
		tz := fs.String("tz", defaultTZ, "fuso horário IANA (env PATCHD_TIMEZONE)")
		clear := fs.Bool("clear", false, "remove a janela do anel")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if *ring < 0 || *ring > store.MaxRing {
			fmt.Fprintf(stderr, "patchd-server: informe -ring (0 a %d)\n", store.MaxRing)
			return exitConfig
		}
		if *clear {
			ok, err := st.DeleteWindow(ctx, *ring)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			if !ok {
				fmt.Fprintf(stderr, "o anel %d não tinha janela\n", *ring)
			} else {
				fmt.Fprintf(stderr, "janela do anel %d removida; os jobs já criados mantêm a janela deles\n", *ring)
			}
			return exitOK
		}
		w, err := parseWindow(*days, *start, *dur, *tz)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		if err := st.SetWindow(ctx, *ring, w, "linha de comando"); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		s, e := w.Next(now, window.MinDuration)
		fmt.Fprintf(stderr, "janela do anel %d: %s; a próxima vai de %s a %s\n", *ring, w,
			s.In(w.Location).Format("02/01 15:04"), e.In(w.Location).Format("02/01 15:04"))
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], ringUsage)
		return exitConfig
	}
}

func pilotLabel(ring int) string {
	if ring == store.PilotRing {
		return " (piloto)"
	}
	return ""
}

func parseWindow(days, start string, dur time.Duration, tz string) (window.Window, error) {
	if days == "" || start == "" || dur == 0 {
		return window.Window{}, errors.New("informe -days, -start e -duration (ou -clear)")
	}
	if tz == "" {
		return window.Window{}, errors.New("informe -tz ou defina PATCHD_TIMEZONE (ex.: America/Porto_Velho)")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return window.Window{}, fmt.Errorf("fuso %q desconhecido: %w", tz, err)
	}
	d, err := window.ParseDays(days)
	if err != nil {
		return window.Window{}, err
	}
	s, err := window.ParseStart(start)
	if err != nil {
		return window.Window{}, err
	}
	w := window.Window{Days: d, Start: s, Duration: dur, Location: loc}
	return w, w.Validate()
}

// ringList mostra os anéis que têm máquinas ou janela.
func ringList(ctx context.Context, st ringStore, now time.Time, out io.Writer) error {
	machines, err := st.Machines(ctx)
	if err != nil {
		return err
	}
	windows, err := st.Windows(ctx)
	if err != nil {
		return err
	}
	count := map[int]int{}
	for _, m := range machines {
		count[m.Ring]++
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ANEL\tMÁQUINAS\tJANELA\tPRÓXIMA\tDEFINIDA POR")
	for r := 0; r <= store.MaxRing; r++ {
		rw, hasWindow := store.WindowFor(windows, r)
		if count[r] == 0 && !hasWindow {
			continue
		}
		win, next, by := "sem janela (jobs só sem -window)", "-", "-"
		if hasWindow {
			win, by = rw.Window.String(), rw.UpdatedBy
			s, e := rw.Window.Next(now, window.MinDuration)
			next = s.In(rw.Window.Location).Format("02/01 15:04")
			if !s.After(now) {
				next = "aberta até " + e.In(rw.Window.Location).Format("15:04")
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", strconv.Itoa(r)+pilotLabel(r), count[r], win, next, by)
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "%d máquina(s) na frota\n", len(machines))
	return nil
}
