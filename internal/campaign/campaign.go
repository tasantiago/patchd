// Package campaign leva um mesmo pedido pelos anéis, em ordem (Aula 8.5, RF-25): primeiro
// o piloto; o anel seguinte só recebe jobs quando todos os do anel atual terminaram com
// sucesso e o tempo de observação passou (liberação automática, decisão da Aula 8.4).
// Qualquer job sem sucesso pausa a campanha: quem decide seguir é o admin.
package campaign

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Estados.
const (
	StateRunning  = "em_andamento"
	StatePaused   = "pausada"
	StateDone     = "concluida"
	StateCanceled = "cancelada"
)

// Limites.
const (
	MaxObserve = 30 * 24 * time.Hour
	MaxSources = 50
)

// Campaign é uma campanha. CurrentIdx é a posição em Rings do anel atual; LaunchedIdx, a
// do último anel cujos jobs já foram criados (-1: nenhum); AcceptedIdx, a do anel cujas
// falhas o admin aceitou ao retomar (-1: nenhum).
type Campaign struct {
	ID          int64
	Name        string
	Type        string
	Sources     []string // update: as fontes (pacotes fonte) a atualizar
	Rings       []int
	UseWindow   bool
	Observe     time.Duration
	TTL         time.Duration // prazo dos jobs sem janela
	State       string
	CurrentIdx  int
	LaunchedIdx int
	AcceptedIdx int
	RingDoneAt  *time.Time
	Detail      string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Ring é o anel atual.
func (c Campaign) Ring() int { return c.Rings[c.CurrentIdx] }

// Validate confere uma campanha nova.
func (c Campaign) Validate() error {
	switch {
	case strings.TrimSpace(c.Name) == "" || len(c.Name) > 100:
		return errors.New("dê um nome à campanha (até 100 caracteres)")
	case !jobs.KnownType(c.Type):
		return fmt.Errorf("tipo de job desconhecido %q", c.Type)
	case c.Type == jobs.TypeUpdate && (len(c.Sources) == 0 || len(c.Sources) > MaxSources):
		return fmt.Errorf("a campanha de update leva de 1 a %d fontes", MaxSources)
	case c.Type != jobs.TypeUpdate && len(c.Sources) > 0:
		return errors.New("só a campanha de update leva fontes")
	case len(c.Rings) == 0:
		return errors.New("informe os anéis, em ordem (ex.: 0,1)")
	case c.Observe < 0 || c.Observe > MaxObserve:
		return fmt.Errorf("observação fora de 0 a %v", MaxObserve)
	case !c.UseWindow && (c.TTL < jobs.MinTTL || c.TTL > jobs.MaxTTL):
		return fmt.Errorf("prazo dos jobs fora de %v a %v", jobs.MinTTL, jobs.MaxTTL)
	}
	for i, r := range c.Rings {
		if r < 0 || r > 9 || (i > 0 && r <= c.Rings[i-1]) {
			return errors.New("anéis de 0 a 9, em ordem crescente e sem repetição (ex.: 0,1,2)")
		}
	}
	return nil
}

// JobState é o que a campanha precisa saber de cada job dela.
type JobState struct {
	ID        int64
	MachineID string
	State     string
	Detail    string
}

// Store é o que a campanha usa do banco.
type Store interface {
	jobs.Queue
	ExpireJobs(ctx context.Context, now time.Time) (int64, error)
	RunningCampaigns(ctx context.Context) ([]Campaign, error)
	SaveCampaign(ctx context.Context, c Campaign, from string) (bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]JobState, error)
	RingMachines(ctx context.Context, ring int) ([]string, error)
}

// Engine avança as campanhas.
type Engine struct {
	Store Store
	Key   ed25519.PrivateKey
	// Pending devolve as pendências avaliadas da máquina (para o update); found false:
	// máquina fora da frota.
	Pending func(ctx context.Context, machineID string) ([]protocol.PendingItem, bool, error)
	// Window devolve a próxima janela do anel da máquina; ok false: o anel não tem janela.
	Window func(ctx context.Context, machineID string, now time.Time) (start, end time.Time, ok bool, err error)
	Logger *slog.Logger
}

// Step avança todas as campanhas em andamento. Um erro numa não impede as outras.
func (e *Engine) Step(ctx context.Context, now time.Time) error {
	if _, err := e.Store.ExpireJobs(ctx, now); err != nil {
		return err
	}
	list, err := e.Store.RunningCampaigns(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range list {
		if err := e.Advance(ctx, &c, now); err != nil {
			errs = append(errs, fmt.Errorf("campanha %d: %w", c.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Advance leva a campanha o mais longe possível agora: cria os jobs do anel atual (se
// ainda não criou), espera eles terminarem, observa, e passa para o anel seguinte.
func (e *Engine) Advance(ctx context.Context, c *Campaign, now time.Time) error {
	if c.State != StateRunning {
		return nil
	}
	before := *c
	defer func() {
		if c.State != before.State || c.CurrentIdx != before.CurrentIdx || c.Detail != before.Detail || c.LaunchedIdx != before.LaunchedIdx {
			c.UpdatedAt = now
		}
	}()
	for steps := 0; steps <= len(c.Rings)+1; steps++ {
		if c.LaunchedIdx < c.CurrentIdx {
			n, skipped, err := e.launch(ctx, c, now)
			if err != nil {
				return err
			}
			if c.State != StateRunning { // o lançamento pausou (anel sem janela)
				return e.save(ctx, c)
			}
			e.log(c, "anel liberado", "anel", c.Ring(), "jobs", n, "sem_pendencia", skipped)
		}
		list, err := e.Store.CampaignJobs(ctx, c.ID, c.CurrentIdx)
		if err != nil {
			return err
		}
		open, failed := 0, []string{}
		for _, j := range list {
			switch {
			case !jobs.Final(j.State):
				open++
			case j.State != jobs.StateDone:
				failed = append(failed, fmt.Sprintf("job %d %s", j.ID, j.State))
			}
		}
		switch {
		case open > 0:
			c.Detail = fmt.Sprintf("anel %d: %d de %d job(s) em aberto", c.Ring(), open, len(list))
			return e.save(ctx, c)
		case len(failed) > 0 && c.AcceptedIdx != c.CurrentIdx:
			c.State = StatePaused
			c.Detail = fmt.Sprintf("anel %d: %d job(s) sem sucesso (%s); retome (campaign resume) para aceitar e seguir", c.Ring(), len(failed), strings.Join(failed, ", "))
			e.log(c, "campanha pausada", "anel", c.Ring(), "sem_sucesso", len(failed))
			return e.save(ctx, c)
		}
		if c.RingDoneAt == nil {
			t := now
			c.RingDoneAt = &t
		}
		last := c.CurrentIdx == len(c.Rings)-1
		if last {
			c.State, c.Detail = StateDone, fmt.Sprintf("todos os anéis concluídos (%s)", ringList(c.Rings))
			e.log(c, "campanha concluída")
			return e.save(ctx, c)
		}
		until := c.RingDoneAt.Add(c.Observe)
		if len(list) > 0 && now.Before(until) {
			c.Detail = fmt.Sprintf("anel %d concluído; observando até %s UTC antes do anel %d", c.Ring(), until.UTC().Format("2006-01-02 15:04"), c.Rings[c.CurrentIdx+1])
			return e.save(ctx, c)
		}
		// Anel seguinte. Um anel sem nenhum job (ninguém com o que fazer) não tem o que observar.
		c.CurrentIdx++
		c.RingDoneAt = nil
	}
	return e.save(ctx, c)
}

// save grava o andamento. Se o admin mudou o estado no meio (pausou, cancelou), a
// gravação não acontece, e a decisão dele vale.
func (e *Engine) save(ctx context.Context, c *Campaign) error {
	_, err := e.Store.SaveCampaign(ctx, *c, StateRunning)
	return err
}

func (e *Engine) log(c *Campaign, msg string, args ...any) {
	if e.Logger != nil {
		e.Logger.Info(msg, append([]any{"campaign_id", c.ID, "campanha", c.Name}, args...)...)
	}
}

// launch cria os jobs do anel atual: um por máquina do anel (no update, só as que têm
// alguma das fontes pendente).
func (e *Engine) launch(ctx context.Context, c *Campaign, now time.Time) (created, skipped int, err error) {
	machines, err := e.Store.RingMachines(ctx, c.Ring())
	if err != nil {
		return 0, 0, err
	}
	for _, id := range machines {
		req := jobs.Request{MachineID: id, Type: c.Type, TTL: c.TTL, By: "campanha " + fmt.Sprint(c.ID),
			CampaignID: c.ID, CampaignIdx: c.CurrentIdx}
		if c.Type == jobs.TypeUpdate {
			items, found, err := e.Pending(ctx, id)
			if err != nil {
				return created, skipped, err
			}
			p := jobs.ParamsFromPending(items, c.Sources)
			if !found || len(p.Packages) == 0 {
				skipped++
				continue
			}
			if req.Params, err = json.Marshal(p); err != nil {
				return created, skipped, err
			}
		}
		if c.UseWindow {
			start, end, ok, err := e.Window(ctx, id, now)
			if err != nil {
				return created, skipped, err
			}
			if !ok {
				c.State = StatePaused
				c.Detail = fmt.Sprintf("anel %d sem janela de manutenção (ring window); defina e retome", c.Ring())
				e.log(c, "campanha pausada", "anel", c.Ring(), "motivo", "sem janela")
				return created, skipped, nil
			}
			req.NotBefore = start
			req.TTL = end.Sub(later(now, start))
		}
		if _, err := jobs.Issue(ctx, e.Store, e.Key, req, now); err != nil {
			if errors.Is(err, jobs.ErrMachineUnavailable) {
				skipped++
				continue
			}
			return created, skipped, err
		}
		created++
	}
	c.LaunchedIdx = c.CurrentIdx
	return created, skipped, nil
}

// Resume retoma uma campanha pausada. Se o anel atual teve falhas, elas ficam aceitas.
func Resume(c *Campaign) error {
	if c.State != StatePaused {
		return fmt.Errorf("a campanha está %s, não pausada", c.State)
	}
	c.State = StateRunning
	if c.LaunchedIdx == c.CurrentIdx {
		c.AcceptedIdx = c.CurrentIdx
	}
	c.Detail = "retomada"
	return nil
}

func ringList(r []int) string {
	s := make([]string, len(r))
	for i, v := range r {
		s[i] = fmt.Sprint(v)
	}
	return strings.Join(s, " → ")
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ParseRings lê "0,1,2".
func ParseRings(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		var r int
		if _, err := fmt.Sscan(strings.TrimSpace(p), &r); err != nil {
			return nil, fmt.Errorf("anel %q inválido", p)
		}
		out = append(out, r)
	}
	if !slices.IsSorted(out) {
		return nil, errors.New("anéis em ordem crescente (ex.: 0,1,2)")
	}
	return out, nil
}
