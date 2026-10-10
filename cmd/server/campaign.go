package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

const campaignUsage = `uso: patchd-server campaign <comando> [opções]

comandos:
  create  cria a campanha: -name "nome" -type rescan|update [-sources fonte,fonte]
          -rings 0,1 [-observe 24h] [-ttl 24h | -window]. O servidor libera o primeiro
          anel no próximo minuto e cada anel seguinte sozinho, quando o anterior termina
          sem falha e a observação passa
  list    lista as campanhas, da mais nova para a mais antiga [-n 20]
  show    mostra a campanha e os jobs de cada anel: -id N
  pause   pausa: -id N (os jobs já criados seguem; nenhum anel novo é liberado)
  resume  retoma uma pausada: -id N (as falhas do anel atual ficam aceitas)
  cancel  cancela: -id N (os jobs ainda pendentes da campanha são cancelados)

update: -sources são pacotes fonte, como aparecem nas pendências do painel (bluez, curl).
Cada máquina do anel recebe os binários instalados dessas fontes que estiverem pendentes
na avaliação do servidor; quem não tem nenhuma pendente fica sem job.
`

// campaignStore é o que os comandos de campanha usam do banco.
type campaignStore interface {
	campaign.Store
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error)
	Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
}

// newCampaignEngine monta o motor das campanhas com o PostgreSQL: as pendências vêm da
// mesma avaliação do painel, e a janela, do anel da máquina.
func newCampaignEngine(pg *store.Postgres, key ed25519.PrivateKey, logger *slog.Logger) *campaign.Engine {
	cs := newComplianceService(pg, logger)
	return &campaign.Engine{Store: pg, Key: key, Logger: logger,
		Pending: func(ctx context.Context, id string) ([]protocol.PendingItem, bool, error) {
			d, found, err := cs.Detail(ctx, id)
			return d.Items, found, err
		},
		Window: func(ctx context.Context, id string, now time.Time) (time.Time, time.Time, bool, error) {
			ring, ok, err := pg.MachineRing(ctx, id)
			if err != nil || !ok {
				return time.Time{}, time.Time{}, false, err
			}
			windows, err := pg.Windows(ctx)
			if err != nil {
				return time.Time{}, time.Time{}, false, err
			}
			rw, ok := store.WindowFor(windows, ring)
			if !ok {
				return time.Time{}, time.Time{}, false, nil
			}
			s, e := rw.Window.Next(now, jobs.MinTTL)
			return s, e, true, nil
		}}
}

// campaignEvery: de quanto em quanto tempo o servidor avança as campanhas.
const campaignEvery = time.Minute

// campaignLocker roda o passo das campanhas sob a trava do banco (Aula 8.5b): dois
// servidores no mesmo banco (um esquecido no outro terminal, ou as duas instâncias da
// RF-37) não liberam o mesmo anel duas vezes.
type campaignLocker interface {
	WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error)
}

// campaignLoop avança as campanhas em andamento a cada minuto, até o encerramento.
func campaignLoop(ctx context.Context, logger *slog.Logger, lock campaignLocker, e *campaign.Engine) {
	t := time.NewTicker(campaignEvery)
	defer t.Stop()
	busy := false // avisa uma vez por troca, não a cada minuto
	for {
		ok, err := lock.WithAdvisoryLock(ctx, store.LockCampaigns, func(ctx context.Context) error {
			return e.Step(ctx, time.Now().UTC())
		})
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("campanhas", "error", err)
		case !ok && err == nil && !busy:
			logger.Warn("campanhas: outro servidor no mesmo banco está com a trava; este não libera anéis enquanto isso")
		}
		busy = !ok && err == nil
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func runCampaign(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, campaignUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: as campanhas exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
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
	return campaignCommand(ctx, store.NewPostgres(pool), args, time.Now().UTC(), stdout, stderr)
}

func campaignCommand(ctx context.Context, st campaignStore, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("campaign "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "create":
		name := fs.String("name", "", "nome da campanha")
		typ := fs.String("type", "", "rescan ou update")
		sources := fs.String("sources", "", "update: pacotes fonte, separados por vírgula")
		rings := fs.String("rings", "", "anéis em ordem, separados por vírgula (ex.: 0,1)")
		observe := fs.Duration("observe", 24*time.Hour, "tempo de observação depois de cada anel concluído")
		ttl := fs.Duration("ttl", 24*time.Hour, "prazo dos jobs sem janela")
		win := fs.Bool("window", false, "jobs na próxima janela de cada anel")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		r, err := campaign.ParseRings(*rings)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: -rings: %v\n", err)
			return exitConfig
		}
		c := campaign.Campaign{Name: strings.TrimSpace(*name), Type: *typ, Rings: r, Observe: *observe, TTL: *ttl,
			UseWindow: *win, CreatedBy: "linha de comando"}
		if *sources != "" {
			for _, s := range strings.Split(*sources, ",") {
				c.Sources = append(c.Sources, strings.TrimSpace(s))
			}
		}
		if err := c.Validate(); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		id, err := st.CreateCampaign(ctx, c)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stderr, "campanha %d criada (%s, anéis %s, observação de %v). O servidor libera o anel %d no próximo minuto;\n"+
			"acompanhe com: patchd-server campaign show -id %d\n", id, c.Type, ringsText(c.Rings), c.Observe, c.Rings[0], id)
		fmt.Fprintln(stdout, id)
		return exitOK
	case "list":
		n := fs.Int("n", 20, "quantas mostrar")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *n < 1 {
			return exitConfig
		}
		list, err := st.Campaigns(ctx, *n)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNOME\tTIPO\tANÉIS\tESTADO\tDETALHE")
		for _, c := range list {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Type, ringsProgress(c), c.State, orDash(c.Detail))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d campanha(s)\n", len(list))
		return exitOK
	case "show", "pause", "resume", "cancel":
		id := fs.Int64("id", 0, "ID da campanha")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *id <= 0 {
			fmt.Fprintln(stderr, "patchd-server: informe -id")
			return exitConfig
		}
		c, ok, err := st.Campaign(ctx, *id)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if !ok {
			fmt.Fprintf(stderr, "patchd-server: campanha %d não existe\n", *id)
			return exitRuntime
		}
		if args[0] == "show" {
			return campaignShow(ctx, st, c, stdout, stderr)
		}
		return campaignChange(ctx, st, c, args[0], stderr)
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], campaignUsage)
		return exitConfig
	}
}

func campaignChange(ctx context.Context, st campaignStore, c campaign.Campaign, op string, stderr io.Writer) int {
	from := c.State
	switch op {
	case "pause":
		if c.State != campaign.StateRunning {
			fmt.Fprintf(stderr, "patchd-server: a campanha está %s; só se pausa uma em andamento\n", c.State)
			return exitRuntime
		}
		c.State, c.Detail = campaign.StatePaused, "pausada pela linha de comando"
	case "resume":
		if err := campaign.Resume(&c); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
	case "cancel":
		if c.State == campaign.StateDone || c.State == campaign.StateCanceled {
			fmt.Fprintf(stderr, "patchd-server: a campanha já está %s\n", c.State)
			return exitRuntime
		}
		c.State, c.Detail = campaign.StateCanceled, "cancelada pela linha de comando"
	}
	ok, err := st.SaveCampaign(ctx, c, from)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if !ok {
		fmt.Fprintln(stderr, "patchd-server: a campanha mudou de estado agora há pouco; confira com campaign show e tente de novo")
		return exitRuntime
	}
	if op == "cancel" {
		canceled := 0
		for idx := range c.Rings {
			list, err := st.CampaignJobs(ctx, c.ID, idx)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			for _, j := range list {
				if j.State == jobs.StatePending {
					if ok, _ := st.CancelJob(ctx, j.ID, "cancelamento da campanha "+strconv.FormatInt(c.ID, 10)); ok {
						canceled++
					}
				}
			}
		}
		fmt.Fprintf(stderr, "campanha %d cancelada; %d job(s) pendente(s) cancelado(s) (os já entregues seguem)\n", c.ID, canceled)
		return exitOK
	}
	fmt.Fprintf(stderr, "campanha %d: %s\n", c.ID, c.State)
	return exitOK
}

func campaignShow(ctx context.Context, st campaignStore, c campaign.Campaign, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "campanha %d: %s\n", c.ID, c.Name)
	fmt.Fprintf(stdout, "tipo: %s", c.Type)
	if len(c.Sources) > 0 {
		fmt.Fprintf(stdout, " (fontes: %s)", strings.Join(c.Sources, ", "))
	}
	when := fmt.Sprintf("prazo de %v", c.TTL)
	if c.UseWindow {
		when = "na janela de cada anel"
	}
	fmt.Fprintf(stdout, "\njobs: %s; observação: %v\nestado: %s\ndetalhe: %s\n", when, c.Observe, c.State, orDash(c.Detail))
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ANEL\tJOB\tMÁQUINA\tESTADO\tDETALHE")
	for idx, ring := range c.Rings {
		list, err := st.CampaignJobs(ctx, c.ID, idx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		mark := ""
		if idx == c.CurrentIdx && c.State != campaign.StateDone {
			mark = " (atual)"
		}
		switch {
		case idx > c.LaunchedIdx:
			fmt.Fprintf(tw, "%d%s\t-\t-\tainda não liberado\t-\n", ring, mark)
		case len(list) == 0:
			fmt.Fprintf(tw, "%d%s\t-\t-\tnenhuma máquina com o que fazer\t-\n", ring, mark)
		}
		for _, j := range list {
			fmt.Fprintf(tw, "%d%s\t%d\t%s\t%s\t%s\n", ring, mark, j.ID, short(j.MachineID), j.State, orDash(j.Detail))
		}
	}
	_ = tw.Flush()
	return exitOK
}

func ringsText(r []int) string {
	s := make([]string, len(r))
	for i, v := range r {
		s[i] = strconv.Itoa(v)
	}
	return strings.Join(s, "→")
}

// ringsProgress marca o anel atual: "0→[1]→2".
func ringsProgress(c campaign.Campaign) string {
	s := make([]string, len(c.Rings))
	for i, v := range c.Rings {
		s[i] = strconv.Itoa(v)
		if i == c.CurrentIdx && c.State != campaign.StateDone {
			s[i] = "[" + s[i] + "]"
		}
	}
	return strings.Join(s, "→")
}
