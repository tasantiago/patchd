package main

import (
	"context"
	"crypto/ed25519"
	"errors"
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
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

const campaignUsage = `uso: patchd-server campaign <comando> [opções]

comandos:
  create  cria a campanha: -name "nome" -type rescan|update|thirdparty [-sources fonte,fonte]
          [-app programa -version versão] -rings 0,1 [-observe 24h] [-ttl 24h | -window].
          O servidor libera o primeiro anel no próximo minuto e cada anel seguinte sozinho,
          quando o anterior termina sem falha e a observação passa
  list    lista as campanhas, da mais nova para a mais antiga [-n 20]
  show    mostra a campanha e os jobs de cada anel: -id N
  pause   pausa: -id N (os jobs já criados seguem; nenhum anel novo é liberado)
  resume  retoma uma pausada: -id N (as falhas do anel atual ficam aceitas)
  cancel  cancela: -id N (os jobs ainda pendentes da campanha são cancelados)

update: -sources são pacotes fonte, como aparecem nas pendências do painel (bluez, curl).
Cada máquina do anel recebe os binários instalados dessas fontes que estiverem pendentes
na avaliação do servidor; quem não tem nenhuma pendente fica sem job.

thirdparty (Aula 14.7): -app é a regra (libreoffice) e -version, a versão aprovada e
guardada (package list). Cada máquina do anel passa pelo package plan com os instaladores
dessa versão: só quem está em ATUALIZAR recebe job; em dia, duplicado, bundle e os outros
ficam fora, com o motivo nas notas da campanha. A cada anel liberado o servidor confere de
novo a aprovação e o arquivo no cache: revogada ou faltando, a campanha pausa.
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
// mesma avaliação do painel, e a janela, do anel da máquina. tp decide as campanhas de
// terceiros (nil: elas pausam, dizendo por quê).
func newCampaignEngine(pg *store.Postgres, key ed25519.PrivateKey, tp campaign.ThirdPartyPlanner, logger *slog.Logger) *campaign.Engine {
	cs := newComplianceService(pg, logger)
	return &campaign.Engine{Store: pg, Key: key, Logger: logger, ThirdParty: tp,
		// Só o que está aprovado em Patch (Aula 13.2), conferido na hora em que cada anel é
		// liberado: uma aprovação revogada no meio da campanha já não entra no anel seguinte.
		Pending: func(ctx context.Context, id string) ([]protocol.PendingItem, bool, error) {
			return approvedPending(ctx, cs.Detail, pg.PatchApprovals, id)
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
	pg := store.NewPostgres(pool)
	tp, err := newCampaigner(pg, look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	return campaignCommand(ctx, pg, tp, args, time.Now().UTC(), stdout, stderr)
}

// newCampaigner monta o planejador das campanhas de terceiros (Aula 14.7): o manifesto
// das regras e a pasta dos instaladores, os mesmos do package plan e do painel.
func newCampaigner(st tpinstall.CampaignStore, look config.Lookup) (*tpinstall.Campaigner, error) {
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	m, err := thirdparty.Load(path)
	if err != nil {
		return nil, fmt.Errorf("manifesto dos programas de terceiros: %w", err)
	}
	dir, err := contentDir(look)
	if err != nil {
		return nil, err
	}
	c := &tpinstall.Campaigner{Store: st, Manifest: m}
	if dir != "" {
		c.Dir = packagesDir(dir)
	}
	return c, nil
}

func campaignCommand(ctx context.Context, st campaignStore, tp campaign.ThirdPartyPlanner, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("campaign "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "create":
		name := fs.String("name", "", "nome da campanha")
		typ := fs.String("type", "", "rescan, update ou thirdparty")
		sources := fs.String("sources", "", "update: pacotes fonte, separados por vírgula")
		app := fs.String("app", "", "thirdparty: a regra do programa (libreoffice)")
		ver := fs.String("version", "", "thirdparty: a versão aprovada e guardada")
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
			UseWindow: *win, CreatedBy: "linha de comando", App: strings.TrimSpace(*app), Version: strings.TrimSpace(*ver)}
		if c.Type == jobs.TypeThirdParty && c.Name == "" && c.App != "" {
			c.Name = c.App + " " + c.Version
		}
		if *sources != "" {
			for _, s := range strings.Split(*sources, ",") {
				c.Sources = append(c.Sources, strings.TrimSpace(s))
			}
		}
		if err := c.Validate(); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		// Terceiros: a versão tem de estar pronta já na criação (a mesma conferência de cada
		// anel), para a campanha não nascer pausada.
		if c.Type == jobs.TypeThirdParty {
			reason := "o servidor está sem as regras de terceiros"
			if tp != nil {
				if reason, err = tp.Ready(ctx, c.App, c.Version); err != nil {
					fmt.Fprintf(stderr, "patchd-server: %v\n", err)
					return exitRuntime
				}
			}
			if reason != "" {
				fmt.Fprintf(stderr, "patchd-server: a campanha não foi criada: %s\n", reason)
				return exitRuntime
			}
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
			typ := c.Type
			if c.Type == jobs.TypeThirdParty {
				typ += " (" + c.Target() + ")"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, typ, ringsProgress(c), c.State, orDash(c.Detail))
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
	n, err := campaign.Change(ctx, st, c, op, "linha de comando")
	switch {
	case errors.Is(err, campaign.ErrState):
		fmt.Fprintf(stderr, "patchd-server: %s\n", strings.TrimPrefix(err.Error(), campaign.ErrState.Error()+": "))
		return exitRuntime
	case errors.Is(err, campaign.ErrChanged):
		fmt.Fprintln(stderr, "patchd-server: a campanha mudou de estado agora há pouco; confira com campaign show e tente de novo")
		return exitRuntime
	case err != nil:
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if op == campaign.OpCancel {
		fmt.Fprintf(stderr, "campanha %d cancelada; %d job(s) pendente(s) cancelado(s) (os já entregues seguem)\n", c.ID, n)
		return exitOK
	}
	st2, _, _ := st.Campaign(ctx, c.ID)
	fmt.Fprintf(stderr, "campanha %d: %s\n", c.ID, st2.State)
	return exitOK
}

func campaignShow(ctx context.Context, st campaignStore, c campaign.Campaign, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "campanha %d: %s\n", c.ID, c.Name)
	fmt.Fprintf(stdout, "tipo: %s", c.Type)
	switch {
	case c.Type == jobs.TypeThirdParty:
		fmt.Fprintf(stdout, " (programa: %s)", c.Target())
	case len(c.Sources) > 0:
		fmt.Fprintf(stdout, " (fontes: %s)", strings.Join(c.Sources, ", "))
	}
	when := fmt.Sprintf("prazo de %v", c.TTL)
	if c.UseWindow {
		when = "na janela de cada anel"
	}
	fmt.Fprintf(stdout, "\njobs: %s; observação: %v\nestado: %s\ndetalhe: %s\n", when, c.Observe, c.State, orDash(c.Detail))
	for _, n := range c.Notes {
		fmt.Fprintf(stdout, "liberação: %s\n", n)
	}
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

// approvedPending: as pendências da máquina que têm a correção aprovada.
func approvedPending(ctx context.Context,
	detail func(context.Context, string) (protocol.ComplianceDetail, bool, error),
	approvals func(context.Context) ([]store.PatchApproval, error), id string) ([]protocol.PendingItem, bool, error) {
	d, found, err := detail(ctx, id)
	if err != nil || !found {
		return nil, found, err
	}
	ap, err := approvals(ctx)
	if err != nil {
		return nil, false, err
	}
	return store.ApprovedOnly(d.Items, ap), true, nil
}
