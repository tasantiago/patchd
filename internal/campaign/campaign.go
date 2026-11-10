// Package campaign leva um mesmo pedido pelos grupos de implantação, em ordem (Aula 8.5, RF-25): primeiro
// o piloto; o grupo de implantação seguinte só recebe jobs quando todos os do grupo de implantação atual terminaram com
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
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/window"
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

// Campanhas de "tudo" (pedido de 04/11/2026): não são um tipo de job, e cada máquina recebe o que
// falta para ela, no próximo check-in (ou na janela do grupo de implantação).
const (
	// TypeOSAll: todas as atualizações do sistema pendentes e aprovadas em Patch. Linux: um
	// update com os pacotes; Windows: um windows_update com os KBs (o que a fonte da política
	// mostra). O macOS fica fora (o patchd não instala atualização do macOS).
	TypeOSAll = "sistema_todas"
	// TypeThirdPartyAll: todos os programas de terceiros aprovados, cada um na versão aprovada
	// mais nova e só onde já está instalado em versão antiga (um job por programa).
	TypeThirdPartyAll = "terceiros_todos"
)

// AppVersion é um programa e a versão aprovada que a campanha leva.
type AppVersion struct{ App, Version string }

// ThirdPartyCatalog lista os programas aprovados (a versão aprovada mais nova de cada um), para
// a campanha TypeThirdPartyAll. O ThirdPartyPlanner que também for um catálogo serve a ela.
type ThirdPartyCatalog interface {
	Approved(ctx context.Context) ([]AppVersion, error)
}

// isAll: a campanha é de "tudo".
func isAll(t string) bool { return t == TypeOSAll || t == TypeThirdPartyAll }

// Campaign é uma campanha. CurrentIdx é a posição em Rings do grupo de implantação atual; LaunchedIdx, a
// do último grupo de implantação cujos jobs já foram criados (-1: nenhum); AcceptedIdx, a do grupo de implantação cujas
// falhas o admin aceitou ao retomar (-1: nenhum).
type Campaign struct {
	ID          int64
	Name        string
	Type        string
	Sources     []string      // update: as fontes (pacotes fonte); windows_update (Aula 16.4): os KBs ("KB5127648")
	App         string        // thirdparty (Aula 14.7): a regra do programa (libreoffice)
	Version     string        // thirdparty: a versão aprovada que a campanha leva a todos os grupos de implantação
	RebootDelay time.Duration // reboot (Aula 16.5): o aviso aos usuários antes do reinício
	Rings       []int
	UseWindow   bool
	// Daily: horário fixo todo dia (ex.: 02:00 por 3 h, no fuso do painel), para todos os grupos
	// de implantação, sem depender da janela de cada um. Nil: com prazo, ou na janela do grupo.
	Daily *window.Window
	// MicrosoftFallback: windows_update e todas do sistema; se a fonte da política falhar ou
	// não oferecer o KB, o job vai com a fonte Microsoft (precisa da reserva ligada em
	// Parâmetros e de um agente que a entenda).
	MicrosoftFallback bool
	// CloseDelay (terceiros, só com horário fixo ou janela): com o programa aberto, o agente
	// avisa quem estiver na sessão, espera isso e fecha o programa (normal, e à força se ele
	// não fechar). 0: não fecha, adia como sempre. Só para agentes que anunciam a capacidade.
	CloseDelay time.Duration
	// AutoResend: quantas vezes, por máquina, o motor reenvia sozinho o job que falhou ou
	// expirou (máquina desligada, rede) antes de contar como falha e pausar; 0: nenhuma. O
	// recusado não volta sozinho (assinatura, sistema: tende a recusar de novo).
	AutoResend int
	// Wake (docs/wake-on-lan.md, Aula 22.3): no início da janela (ou do horário fixo), ligar
	// pela rede as máquinas do grupo desligadas com job da campanha; quando os jobs delas
	// terminam, desligar só as que o patchd ligou e sem ninguém conectado. Só com janela ou
	// horário fixo.
	Wake        bool
	Observe     time.Duration
	TTL         time.Duration // prazo dos jobs sem janela
	State       string
	CurrentIdx  int
	LaunchedIdx int
	AcceptedIdx int
	RingDoneAt  *time.Time
	Detail      string
	Notes       []string // um resumo por grupo de implantação liberado: jobs criados e quem ficou fora (Aula 14.7)
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// ArchivedAt: arquivada (só a encerrada): fora da lista, sem perder nada. nil: na lista.
	ArchivedAt *time.Time
}

// Finished: concluída ou cancelada (o motor não mexe mais nela).
func (c Campaign) Finished() bool { return c.State == StateDone || c.State == StateCanceled }

// Ring é o grupo de implantação atual.
func (c Campaign) Ring() int { return c.Rings[c.CurrentIdx] }

// Target diz o que a campanha leva: as fontes do update, ou o programa e a versão.
func (c Campaign) Target() string {
	switch c.Type {
	case TypeOSAll:
		return "todas as atualizações do sistema aprovadas em Patch"
	case TypeThirdPartyAll:
		return "todos os programas de terceiros aprovados, onde já estão instalados"
	}
	if c.Type == jobs.TypeThirdParty {
		return c.App + " " + c.Version
	}
	if c.Type == jobs.TypeReboot {
		return fmt.Sprintf("onde houver reinício pendente, com aviso de %d min", int(c.RebootDelay/time.Minute))
	}
	return strings.Join(c.Sources, ", ")
}

// Validate confere uma campanha nova.
func (c Campaign) Validate() error {
	switch {
	case strings.TrimSpace(c.Name) == "" || len(c.Name) > 100:
		return errors.New("dê um nome à campanha (até 100 caracteres)")
	case !jobs.KnownType(c.Type) && !isAll(c.Type):
		return fmt.Errorf("tipo de job desconhecido %q", c.Type)
	case c.Type == jobs.TypeReboot && (c.RebootDelay < jobs.MinRebootDelay || c.RebootDelay > jobs.MaxRebootDelay):
		return fmt.Errorf("o aviso do reinício fica entre %v e %v", jobs.MinRebootDelay, jobs.MaxRebootDelay)
	case c.Type != jobs.TypeReboot && c.RebootDelay != 0:
		return errors.New("só a campanha de reinício leva aviso")
	case c.Wake && !c.UseWindow && c.Daily == nil:
		return errors.New("ligar pela rede vai com a janela do grupo ou o horário fixo (de madrugada), não com o prazo")
	case c.AutoResend < 0 || c.AutoResend > MaxAutoResend:
		return fmt.Errorf("os reenvios automáticos vão de 0 a %d", MaxAutoResend)
	case c.Type == jobs.TypeServerURL:
		return errors.New("campanha de troca de endereço ainda não existe: use job create -type server_url -ring N ou o painel")
	case c.Type == jobs.TypeBIOSPassword:
		return errors.New("a senha da BIOS é gravada máquina a máquina, pelo painel (aba Hardware)")
	case c.Type == jobs.TypeUpdate && (len(c.Sources) == 0 || len(c.Sources) > MaxSources):
		return fmt.Errorf("a campanha de update leva de 1 a %d fontes", MaxSources)
	case c.Type == jobs.TypeWinUpdate && (len(c.Sources) == 0 || len(c.Sources) > jobs.MaxWinUpdates):
		return fmt.Errorf("a campanha do Windows leva de 1 a %d KBs", jobs.MaxWinUpdates)
	case c.Type != jobs.TypeUpdate && c.Type != jobs.TypeWinUpdate && len(c.Sources) > 0:
		return errors.New("só a campanha de update leva fontes, e a do Windows, KBs")
	case c.Type == jobs.TypeThirdParty && (c.App == "" || c.Version == "" || len(c.App) > 64 || len(c.Version) > 64):
		return errors.New("a campanha de terceiros leva o programa e a versão aprovada (-app e -version)")
	case c.Type != jobs.TypeThirdParty && (c.App != "" || c.Version != ""):
		return errors.New("só a campanha de terceiros leva programa e versão")
	case len(c.Rings) == 0:
		return errors.New("informe os grupos de implantação, em ordem (ex.: 0,1)")
	case c.Observe < 0 || c.Observe > MaxObserve:
		return fmt.Errorf("observação fora de 0 a %v", MaxObserve)
	case c.CloseDelay != 0 && c.Type != jobs.TypeThirdParty && c.Type != TypeThirdPartyAll:
		return errors.New("fechar o programa aberto só vale nas campanhas de terceiros")
	case c.CloseDelay != 0 && !c.UseWindow && c.Daily == nil:
		return errors.New("fechar o programa aberto precisa de horário: em horário fixo (de madrugada) ou na janela do grupo")
	case c.CloseDelay != 0 && (c.CloseDelay < jobs.MinCloseNotice*time.Minute || c.CloseDelay > jobs.MaxCloseNotice*time.Minute):
		return fmt.Errorf("o aviso antes de fechar o programa fica entre %d e %d minutos", jobs.MinCloseNotice, jobs.MaxCloseNotice)
	case c.MicrosoftFallback && c.Type != jobs.TypeWinUpdate && c.Type != TypeOSAll:
		return errors.New("a fonte reserva da Microsoft só vale nas campanhas do Windows")
	case c.UseWindow && c.Daily != nil:
		return errors.New("escolha um: na janela de cada grupo ou em horário fixo")
	case !c.UseWindow && c.Daily == nil && (c.TTL < jobs.MinTTL || c.TTL > jobs.MaxTTL):
		return fmt.Errorf("prazo dos jobs fora de %v a %v", jobs.MinTTL, jobs.MaxTTL)
	}
	if c.Daily != nil {
		if err := c.Daily.Validate(); err != nil {
			return fmt.Errorf("horário fixo: %w", err)
		}
	}
	if c.Type == jobs.TypeWinUpdate {
		for _, kb := range c.Sources {
			if n, ok := jobs.NormalizeKB(kb); !ok || n != kb {
				return fmt.Errorf("KB %q fora do formato (KB5127648)", kb)
			}
		}
	}
	for i, r := range c.Rings {
		if r < 0 || r > 9 || (i > 0 && r <= c.Rings[i-1]) {
			return errors.New("grupos de implantação de 0 a 9, em ordem crescente e sem repetição (ex.: 0,1,2)")
		}
	}
	return nil
}

// JobState é o que a campanha precisa saber de cada job dela.
type JobState struct {
	ID         int64
	MachineID  string
	State      string
	Detail     string
	FinishedAt *time.Time // quando encerrou (Aula 14.9: o adiado conta o intervalo daqui)
	// Unblocked: o agente avisou que o programa que adiou o job fechou; a nova tentativa sai
	// na hora, sem esperar o RetryAfter.
	Unblocked *time.Time
	// Slot: o que o job leva, quando uma máquina recebe mais de um na mesma campanha (na de
	// todos os programas de terceiros, o programa). Vazio: um job por máquina.
	Slot string
	// NotBefore: o início da janela (ou do horário fixo) do job; nil: sem janela.
	NotBefore *time.Time
}

// WakeRecord (Aula 22.3): o ligar e o desligar de uma máquina num grupo da campanha.
type WakeRecord struct {
	CampaignID  int64
	Idx         int
	MachineID   string
	RequestedAt time.Time
	// WakeRequestID: o pedido de ligar (0: não saiu; o motivo em Problem).
	WakeRequestID int64
	Problem       string
	// ShutdownJobID: o job de desligar (0: ainda não, ou não vai; o motivo em ShutdownNote).
	ShutdownJobID int64
	ShutdownNote  string
	// Lidos com o registro: a máquina respondeu ao pacote (o primeiro check-in depois dele) e
	// a situação do job de desligar.
	AnsweredAt                    *time.Time
	ShutdownState, ShutdownDetail string
}

// ShutdownDecided: o desligar já foi decidido (o job saiu, ou ficou anotado por que não).
func (w WakeRecord) ShutdownDecided() bool { return w.ShutdownJobID != 0 || w.ShutdownNote != "" }

// Store é o que a campanha usa do banco.
type Store interface {
	jobs.Queue
	ExpireJobs(ctx context.Context, now time.Time) (int64, error)
	RunningCampaigns(ctx context.Context) ([]Campaign, error)
	SaveCampaign(ctx context.Context, c Campaign, from string) (bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]JobState, error)
	RingMachines(ctx context.Context, ring int) ([]string, error)
	DismissJob(ctx context.Context, id int64, why string) (bool, error)
	// TakeResend: o pedido de reenvio do admin (os motivos), esvaziado ao ler.
	TakeResend(ctx context.Context, id int64) ([]string, error)
	// Ligar e desligar na janela (Aula 22.3).
	SaveCampaignWake(ctx context.Context, w WakeRecord) error
	CampaignWakes(ctx context.Context, id int64, idx int) ([]WakeRecord, error)
	LastSeen(ctx context.Context, ids []string) (map[string]time.Time, error)
}

// Novas tentativas dos jobs adiados (Aula 14.9): o agente responde "adiado" quando não deu
// agora (programa aberto, outro instalador rodando). A campanha pede de novo depois de
// RetryAfter, até MaxAttempts jobs por máquina no grupo de implantação; esgotadas, conta como falha e pausa.
const (
	RetryAfter  = time.Hour
	MaxAttempts = 24
)

// Reenvio das falhas: os motivos (o estado do job, ou o adiado que esgotou as tentativas).
const (
	ReasonFailed    = jobs.StateFailed
	ReasonExpired   = jobs.StateExpired
	ReasonRejected  = jobs.StateRejected
	ReasonPostponed = "adiado no limite de tentativas"
	// MaxAutoResend: o maior AutoResend.
	MaxAutoResend = 5
)

// ResendReasons: os motivos que o admin pode reenviar, na ordem da tela.
var ResendReasons = []string{ReasonExpired, ReasonFailed, ReasonRejected, ReasonPostponed}

// ThirdPartyPlanner decide a campanha de terceiros (Aula 14.7). Ready confere, a cada grupo de implantação
// liberado, que a versão continua aprovada e que o instalador confere no cache: reason não
// vazio pausa a campanha com o motivo. Job decide a máquina: os parâmetros do job, ou por
// que ela fica fora (skip: "em dia", "duplicado", "sem o programa"...).
type ThirdPartyPlanner interface {
	Ready(ctx context.Context, app, version string) (reason string, err error)
	Job(ctx context.Context, machineID, app, version string) (p *jobs.ThirdPartyParams, skip string, err error)
}

// Engine avança as campanhas.
type Engine struct {
	Store Store
	Key   ed25519.PrivateKey
	// Pending devolve as pendências avaliadas da máquina (para o update); found false:
	// máquina fora da frota.
	Pending func(ctx context.Context, machineID string) ([]protocol.PendingItem, bool, error)
	// Window devolve a próxima janela do grupo de implantação da máquina; ok false: o grupo de implantação não tem janela.
	Window func(ctx context.Context, machineID string, now time.Time) (start, end time.Time, ok bool, err error)
	// Scan devolve a busca atual da máquina (Aula 16.4, campanha do Windows): o job leva os
	// UpdateIDs que ela mostra na fonte da política. nil: as máquinas ficam fora.
	Scan func(ctx context.Context, machineID string) (protocol.PatchScanReport, bool, error)
	// ThirdParty decide a campanha thirdparty; nil: elas pausam no primeiro grupo de implantação.
	ThirdParty ThirdPartyPlanner
	// MicrosoftAllowed: a fonte reserva da Microsoft está ligada em Parâmetros. nil: desligada.
	MicrosoftAllowed func() bool
	// CanCloseApps: o agente da máquina anunciou que fecha o programa com aviso. nil: ninguém.
	CanCloseApps func(ctx context.Context, machineID string) (bool, error)
	// Wake liga a máquina pela rede (Aula 22.3, o pedido do internal/wakereq): o ID do pedido,
	// ou, sem ele, por que não saiu. nil: as campanhas com Wake não ligam ninguém.
	Wake func(ctx context.Context, machineID, by string) (requestID int64, problem string, err error)
	// CanShutdown: o agente da máquina anunciou o job desligar. nil: ninguém.
	CanShutdown func(ctx context.Context, machineID string) (bool, error)
	Logger      *slog.Logger
}

// closeApps põe o "fechar o programa com aviso" no job de terceiros, quando a campanha pede,
// a regra lista os processos e o agente sabe fazer. Senão, o job vai como sempre (adia).
func (e *Engine) closeApps(ctx context.Context, c *Campaign, id string, p *jobs.ThirdPartyParams) error {
	if c.CloseDelay == 0 || len(p.Processes) == 0 || e.CanCloseApps == nil {
		return nil
	}
	ok, err := e.CanCloseApps(ctx, id)
	if err != nil || !ok {
		return err
	}
	p.Close = &jobs.CloseApps{NoticeMinutes: int(c.CloseDelay / time.Minute)}
	return nil
}

// winUpdates monta o job do Windows: pela fonte da política e, se ela não serve por um motivo
// que a Microsoft resolve e a campanha pede, pela fonte Microsoft.
func (e *Engine) winUpdates(c *Campaign, rep protocol.PatchScanReport, kbs []string) (jobs.WinUpdateParams, string) {
	p, skip := jobs.PolicyWinUpdates(rep, kbs)
	if skip == "" {
		// A política oferece hoje; se na hora não entregar (o KB sumiu do WSUS, o download
		// falha), o agente que sabe refaz pela Microsoft no mesmo job.
		if c.MicrosoftFallback && e.MicrosoftAllowed != nil && e.MicrosoftAllowed() &&
			rep.MicrosoftUpdate != nil && rep.MicrosoftUpdate.Available && rep.MicrosoftUpdate.InJob {
			p.Fallback = true
		}
		return p, skip
	}
	if !jobs.FallbackReason(skip) || !c.MicrosoftFallback {
		return p, skip
	}
	if e.MicrosoftAllowed == nil || !e.MicrosoftAllowed() {
		return p, skip + " (reserva da Microsoft desligada em Parâmetros)"
	}
	return jobs.MicrosoftWinUpdates(rep, kbs)
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

// Advance leva a campanha o mais longe possível agora: cria os jobs do grupo de implantação atual (se
// ainda não criou), espera eles terminarem, observa, e passa para o grupo de implantação seguinte.
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
			n, out, err := e.launch(ctx, c, now)
			if err != nil {
				return err
			}
			if c.State != StateRunning { // o lançamento pausou (grupo de implantação sem janela, versão não pronta)
				return e.save(ctx, c)
			}
			note := RingNote(c.Ring(), n, out)
			c.Notes = append(slices.Clone(c.Notes), note)
			e.log(c, "grupo de implantação liberado", "anel", c.Ring(), "jobs", n, "resumo", note)
		}
		list, err := e.Store.CampaignJobs(ctx, c.ID, c.CurrentIdx)
		if err != nil {
			return err
		}
		st := ringStatus(list, now, c.AutoResend)
		// O reenvio pedido pelo admin: os sem sucesso dos motivos escolhidos vão de novo.
		reasons, err := e.Store.TakeResend(ctx, c.ID)
		if err != nil {
			return err
		}
		if len(reasons) > 0 {
			var pick []JobState
			for _, f := range st.failedJobs {
				if slices.Contains(reasons, f.reason) {
					pick = append(pick, f.job)
				}
			}
			n, err := e.retry(ctx, c, pick, now)
			if err != nil {
				return err
			}
			c.Notes = append(slices.Clone(c.Notes), fmt.Sprintf("grupo de implantação %d: reenvio pedido (%s): %d job(s) novo(s), %d dispensado(s) (a máquina já não precisa)",
				c.Ring(), strings.Join(reasons, ", "), n, len(pick)-n))
			e.log(c, "reenvio das falhas", "anel", c.Ring(), "motivos", reasons, "jobs", n)
			if c.State != StateRunning {
				return e.save(ctx, c)
			}
			if list, err = e.Store.CampaignJobs(ctx, c.ID, c.CurrentIdx); err != nil {
				return err
			}
			st = ringStatus(list, now, c.AutoResend)
		}
		if len(st.retry) > 0 {
			reissued, err := e.retry(ctx, c, st.retry, now)
			if err != nil {
				return err
			}
			if c.State != StateRunning { // a versão deixou de estar pronta, ou o grupo de implantação ficou sem janela
				return e.save(ctx, c)
			}
			st.open += reissued
		}
		if c.Wake {
			if err := e.wakeStep(ctx, c, list, now); err != nil {
				return err
			}
		}
		switch {
		case st.open > 0 || st.waiting > 0:
			c.Detail = fmt.Sprintf("grupo de implantação %d: %d de %d job(s) em aberto", c.Ring(), st.open+st.waiting, st.machines)
			if st.waiting > 0 {
				c.Detail += fmt.Sprintf("; %d adiada(s), nova tentativa a partir de %s UTC", st.waiting, st.next.UTC().Format("2006-01-02 15:04"))
			}
			return e.save(ctx, c)
		case len(st.failed) > 0 && c.AcceptedIdx != c.CurrentIdx:
			c.State = StatePaused
			// O resumo e o que fazer primeiro; a lista dos jobs (centenas, num parque grande) no fim.
			c.Detail = fmt.Sprintf("grupo de implantação %d: %d job(s) sem sucesso (%s); retome (campaign resume) para aceitar e seguir. Jobs: %s",
				c.Ring(), len(st.failed), st.failedSummary(), strings.Join(st.failed, ", "))
			e.log(c, "campanha pausada", "anel", c.Ring(), "sem_sucesso", len(st.failed))
			return e.save(ctx, c)
		}
		if c.RingDoneAt == nil {
			t := now
			c.RingDoneAt = &t
		}
		last := c.CurrentIdx == len(c.Rings)-1
		if last {
			c.State, c.Detail = StateDone, fmt.Sprintf("todos os grupos de implantação concluídos (%s)", ringList(c.Rings))
			e.log(c, "campanha concluída")
			return e.save(ctx, c)
		}
		until := c.RingDoneAt.Add(c.Observe)
		if len(list) > 0 && now.Before(until) {
			c.Detail = fmt.Sprintf("grupo de implantação %d concluído; observando até %s UTC antes do grupo de implantação %d", c.Ring(), until.UTC().Format("2006-01-02 15:04"), c.Rings[c.CurrentIdx+1])
			return e.save(ctx, c)
		}
		// Grupo de implantação seguinte. Um grupo de implantação sem nenhum job (ninguém com o que fazer) não tem o que observar.
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

// launch cria os jobs do grupo de implantação atual: um por máquina do grupo de implantação (no update, só as que têm
// alguma das fontes pendente; no thirdparty, só as que o plano manda atualizar). out conta
// as máquinas que ficaram fora, por motivo.
func (e *Engine) launch(ctx context.Context, c *Campaign, now time.Time) (created int, out map[string]int, err error) {
	out = map[string]int{}
	if ok, err := e.ready(ctx, c, fmt.Sprintf("grupo de implantação %d não liberado", c.Ring())); err != nil || !ok {
		return 0, out, err
	}
	machines, err := e.Store.RingMachines(ctx, c.Ring())
	if err != nil {
		return 0, out, err
	}
	if c.Type == TypeThirdPartyAll {
		apps, notReady, err := e.readyApps(ctx)
		if err != nil {
			return 0, out, err
		}
		for _, r := range notReady {
			out[r]++
		}
		for _, id := range machines {
			n, skips, err := e.issueApps(ctx, c, id, apps, "", now)
			if err != nil {
				return created, out, err
			}
			if c.State != StateRunning {
				return created, out, nil
			}
			created += n
			for _, sk := range skips {
				out[sk]++
			}
		}
		c.LaunchedIdx = c.CurrentIdx
		return created, out, nil
	}
	for _, id := range machines {
		ok, skip, err := e.issue(ctx, c, id, now)
		switch {
		case err != nil:
			return created, out, err
		case c.State != StateRunning: // grupo de implantação sem janela
			return created, out, nil
		case ok:
			created++
		default:
			out[skip]++
		}
	}
	c.LaunchedIdx = c.CurrentIdx
	return created, out, nil
}

// ready confere, no thirdparty, que a versão continua pronta (Aula 14.7): a aprovação manda
// a cada grupo de implantação e a cada nova tentativa. Não pronta: a campanha pausa com o motivo.
func (e *Engine) ready(ctx context.Context, c *Campaign, what string) (bool, error) {
	if c.Type != jobs.TypeThirdParty {
		return true, nil
	}
	reason := "o servidor está sem as regras de terceiros"
	if e.ThirdParty != nil {
		var err error
		if reason, err = e.ThirdParty.Ready(ctx, c.App, c.Version); err != nil {
			return false, err
		}
	}
	if reason == "" {
		return true, nil
	}
	c.State = StatePaused
	c.Detail = fmt.Sprintf("%s: %s; resolva e retome (campaign resume)", what, reason)
	e.log(c, "campanha pausada", "anel", c.Ring(), "motivo", reason)
	return false, nil
}

// issue cria o job de uma máquina no grupo de implantação atual; sem job, skip diz por quê. Um grupo de implantação sem
// janela pausa a campanha (c.State muda).
func (e *Engine) issue(ctx context.Context, c *Campaign, id string, now time.Time) (bool, string, error) {
	req := jobs.Request{MachineID: id, Type: c.Type, TTL: c.TTL, By: "campanha " + fmt.Sprint(c.ID),
		CampaignID: c.ID, CampaignIdx: c.CurrentIdx}
	switch c.Type {
	case TypeOSAll:
		return e.issueOSAll(ctx, c, req, id, now)
	case jobs.TypeUpdate:
		items, found, err := e.Pending(ctx, id)
		if err != nil {
			return false, "", err
		}
		p := jobs.ParamsFromPending(items, c.Sources)
		if !found || len(p.Packages) == 0 {
			return false, "sem pendência aprovada", nil
		}
		if req.Params, err = json.Marshal(p); err != nil {
			return false, "", err
		}
	case jobs.TypeWinUpdate:
		// Os KBs da campanha que corrigem alguma CVE aprovada desta máquina (Aula 13.2: a
		// aprovação vale na hora em que o grupo de implantação é liberado) e que a busca dela mostra na fonte
		// da política (Aula 16.2: fonte falhando, nem tenta).
		items, found, err := e.Pending(ctx, id)
		if err != nil {
			return false, "", err
		}
		kbs := jobs.KBsInPending(items, c.Sources)
		if !found || len(kbs) == 0 {
			return false, "sem pendência aprovada", nil
		}
		if e.Scan == nil {
			return false, "sem busca", nil
		}
		rep, ok, err := e.Scan(ctx, id)
		if err != nil {
			return false, "", err
		}
		if !ok {
			return false, "sem busca", nil
		}
		p, skip := e.winUpdates(c, rep, kbs)
		if skip != "" {
			return false, skip, nil
		}
		if req.Params, err = json.Marshal(p); err != nil {
			return false, "", err
		}
	case jobs.TypeReboot:
		// Aula 16.5: só quem a busca atual mostra com reinício pendente. O agente confere de
		// novo na hora; se já não houver, o job volta dispensado, não falho.
		if e.Scan == nil {
			return false, "sem busca", nil
		}
		rep, ok, err := e.Scan(ctx, id)
		if err != nil {
			return false, "", err
		}
		switch {
		case !ok:
			return false, "sem busca", nil
		case rep.Reboot == nil || rep.Reboot.Pending == nil:
			return false, "reinício pendente desconhecido", nil
		case !*rep.Reboot.Pending:
			return false, "sem reinício pendente", nil
		}
		if req.Params, err = jobs.RebootParamsJSON(c.RebootDelay); err != nil {
			return false, "", err
		}
	case jobs.TypeThirdParty:
		p, skip, err := e.ThirdParty.Job(ctx, id, c.App, c.Version)
		if err != nil || p == nil {
			return false, skip, err
		}
		if err := e.closeApps(ctx, c, id, p); err != nil {
			return false, "", err
		}
		if req.Params, err = json.Marshal(p); err != nil {
			return false, "", err
		}
	}
	return e.send(ctx, c, req, id, now)
}

// send cria o job pedido (na janela do grupo de implantação, se a campanha usa janela).
func (e *Engine) send(ctx context.Context, c *Campaign, req jobs.Request, id string, now time.Time) (bool, string, error) {
	if c.UseWindow {
		start, end, ok, err := e.Window(ctx, id, now)
		if err != nil {
			return false, "", err
		}
		if !ok {
			c.State = StatePaused
			c.Detail = fmt.Sprintf("grupo de implantação %d sem janela de manutenção (ring window); defina e retome", c.Ring())
			e.log(c, "campanha pausada", "anel", c.Ring(), "motivo", "sem janela")
			return false, "", nil
		}
		req.NotBefore = start
		req.TTL = end.Sub(later(now, start))
	}
	if c.Daily != nil {
		start, end := c.Daily.Next(now, jobs.MinTTL)
		req.NotBefore = start
		req.TTL = end.Sub(later(now, start))
	}
	if _, err := jobs.Issue(ctx, e.Store, e.Key, req, now); err != nil {
		if errors.Is(err, jobs.ErrMachineUnavailable) {
			return false, "aposentada", nil
		}
		return false, "", err
	}
	return true, "", nil
}

// ringState: o grupo de implantação atual, máquina a máquina, pelo job mais recente de cada uma.
type ringState struct {
	machines, open, waiting int
	failed                  []string
	reasons                 map[string]int // sem sucesso por motivo: expirado, rejeitado, falhou...
	failedJobs              []failedJob    // os sem sucesso, para o reenvio pedido pelo admin
	retry                   []JobState     // adiados cujo intervalo passou
	next                    time.Time      // a próxima nova tentativa entre os que esperam
}

type failedJob struct {
	job    JobState
	reason string
}

func ringStatus(list []JobState, now time.Time, autoResend int) ringState {
	latest, attempts, fails := map[string]JobState{}, map[string]int{}, map[string]int{}
	for _, j := range list {
		k := j.MachineID + "\x00" + j.Slot // uma máquina com vários programas: cada um conta
		attempts[k]++
		if j.State == jobs.StateFailed || j.State == jobs.StateExpired {
			fails[k]++
		}
		if cur, ok := latest[k]; !ok || j.ID > cur.ID {
			latest[k] = j
		}
	}
	st := ringState{machines: len(latest)}
	for _, m := range slices.Sorted(maps.Keys(latest)) {
		j := latest[m]
		switch {
		case !jobs.Final(j.State):
			st.open++
		case j.State == jobs.StatePostponed && attempts[m] >= MaxAttempts:
			st.failed = append(st.failed, fmt.Sprintf("job %d adiado na tentativa %d de %d", j.ID, attempts[m], MaxAttempts))
			st.fail(j, ReasonPostponed)
		case (j.State == jobs.StateFailed || j.State == jobs.StateExpired) && fails[m] <= autoResend:
			// Reenvio automático: o expirado (máquina desligada) vai de novo já; o que falhou,
			// depois do intervalo.
			at := now
			if j.State == jobs.StateFailed && j.FinishedAt != nil {
				at = j.FinishedAt.Add(RetryAfter)
			}
			if now.Before(at) {
				st.waiting++
				if st.next.IsZero() || at.Before(st.next) {
					st.next = at
				}
				continue
			}
			st.retry = append(st.retry, j)
		case j.State == jobs.StatePostponed:
			at := now
			if j.FinishedAt != nil {
				at = j.FinishedAt.Add(RetryAfter)
			}
			if j.Unblocked != nil && j.Unblocked.Before(at) { // tentar assim que fechar
				at = *j.Unblocked
			}
			if now.Before(at) {
				st.waiting++
				if st.next.IsZero() || at.Before(st.next) {
					st.next = at
				}
				continue
			}
			st.retry = append(st.retry, j)
		case j.State == jobs.StateDone, j.State == jobs.StateDismissed:
		default:
			st.failed = append(st.failed, fmt.Sprintf("job %d %s", j.ID, j.State))
			st.fail(j, j.State)
		}
	}
	return st
}

func (st *ringState) fail(j JobState, reason string) {
	if st.reasons == nil {
		st.reasons = map[string]int{}
	}
	st.reasons[reason]++
	st.failedJobs = append(st.failedJobs, failedJob{job: j, reason: reason})
}

// FailedByReason: os jobs sem sucesso do grupo de implantação (a lista de CampaignJobs), por
// motivo, como o motor conta (o último job de cada máquina; os que ainda vão ter reenvio
// automático não contam).
func FailedByReason(list []JobState, now time.Time, autoResend int) map[string]int {
	return ringStatus(list, now, autoResend).reasons
}

// failedSummary: "231 expirado, 14 rejeitado, 2 falhou", do motivo mais comum ao menos.
func (st ringState) failedSummary() string {
	keys := slices.Collect(maps.Keys(st.reasons))
	slices.SortFunc(keys, func(a, b string) int {
		if d := st.reasons[b] - st.reasons[a]; d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", st.reasons[k], k)
	}
	return strings.Join(parts, ", ")
}

// retry pede de novo às máquinas adiadas cujo intervalo passou. O plano é refeito: se a
// máquina não precisa mais (em dia, sem o programa, sem pendência), o adiado vira
// "dispensado" e ela sai do grupo de implantação resolvida. Devolve quantos jobs novos foram criados.
func (e *Engine) retry(ctx context.Context, c *Campaign, list []JobState, now time.Time) (int, error) {
	if ok, err := e.ready(ctx, c, fmt.Sprintf("grupo de implantação %d: nova tentativa suspensa", c.Ring())); err != nil || !ok {
		return 0, err
	}
	created := 0
	var apps []AppVersion
	if c.Type == TypeThirdPartyAll {
		var err error
		if apps, _, err = e.readyApps(ctx); err != nil {
			return 0, err
		}
	}
	for _, j := range list {
		var ok bool
		var skip string
		var err error
		if c.Type == TypeThirdPartyAll {
			// A nova tentativa é do mesmo programa (o slot), não de todos de novo.
			var n int
			var skips []string
			n, skips, err = e.issueApps(ctx, c, j.MachineID, apps, j.Slot, now)
			ok = n > 0
			if !ok {
				skip = "o programa não está mais aprovado ou pronto"
				if len(skips) > 0 {
					skip = skips[0]
				}
			}
		} else {
			ok, skip, err = e.issue(ctx, c, j.MachineID, now)
		}
		switch {
		case err != nil:
			return created, err
		case c.State != StateRunning:
			return created, nil
		case ok:
			created++
			e.log(c, "nova tentativa", "anel", c.Ring(), "machine_id", j.MachineID, "adiado", j.ID)
		default:
			if _, err := e.Store.DismissJob(ctx, j.ID, "nova tentativa dispensada: "+skip); err != nil {
				return created, err
			}
			e.log(c, "nova tentativa dispensada", "anel", c.Ring(), "machine_id", j.MachineID, "adiado", j.ID, "motivo", skip)
		}
	}
	return created, nil
}

// RingNote resume a liberação de um grupo de implantação: "grupo de implantação 0: 1 job; fora: 1 em dia, 2 sem o programa".
func RingNote(ring, created int, out map[string]int) string {
	s := fmt.Sprintf("grupo de implantação %d: %d job(s)", ring, created)
	if len(out) == 0 {
		return s
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(out)) {
		parts = append(parts, fmt.Sprintf("%d %s", out[k], k))
	}
	return s + "; fora: " + strings.Join(parts, ", ")
}

// Resume retoma uma campanha pausada. Se o grupo de implantação atual teve falhas, elas ficam aceitas.
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
			return nil, fmt.Errorf("grupo de implantação %q inválido", p)
		}
		out = append(out, r)
	}
	if !slices.IsSorted(out) {
		return nil, errors.New("grupos de implantação em ordem crescente (ex.: 0,1,2)")
	}
	return out, nil
}

// issueOSAll: o job do sistema da máquina com todas as pendências aprovadas dela. Linux: os
// pacotes (até jobs.MaxPackages, as exploradas primeiro); Windows: os KBs que a fonte da
// política mostra (até jobs.MaxWinUpdates).
func (e *Engine) issueOSAll(ctx context.Context, c *Campaign, req jobs.Request, id string, now time.Time) (bool, string, error) {
	items, found, err := e.Pending(ctx, id)
	if err != nil {
		return false, "", err
	}
	if !found || len(items) == 0 {
		return false, "sem pendência aprovada", nil
	}
	if p := jobs.ParamsFromPendingAll(items); len(p.Packages) > 0 {
		if len(p.Packages) > jobs.MaxPackages {
			p.Packages = p.Packages[:jobs.MaxPackages] // o resto fica para a próxima campanha
		}
		req.Type = jobs.TypeUpdate
		if req.Params, err = json.Marshal(p); err != nil {
			return false, "", err
		}
		return e.send(ctx, c, req, id, now)
	}
	kbs := jobs.KBsInPendingAll(items)
	if len(kbs) == 0 {
		return false, "sem instalação pelo patchd (macOS)", nil
	}
	if e.Scan == nil {
		return false, "sem busca", nil
	}
	rep, ok, err := e.Scan(ctx, id)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, "sem busca", nil
	}
	if len(kbs) > jobs.MaxWinUpdates {
		kbs = kbs[:jobs.MaxWinUpdates]
	}
	p, skip := e.winUpdates(c, rep, kbs)
	if skip != "" {
		return false, skip, nil
	}
	req.Type = jobs.TypeWinUpdate
	if req.Params, err = json.Marshal(p); err != nil {
		return false, "", err
	}
	return e.send(ctx, c, req, id, now)
}

// readyApps: os programas aprovados que estão prontos agora (aprovação em vigor, instalador
// no cache). notReady: um resumo de cada um que ficou fora, para as notas da campanha.
func (e *Engine) readyApps(ctx context.Context) (apps []AppVersion, notReady []string, err error) {
	cat, ok := e.ThirdParty.(ThirdPartyCatalog)
	if e.ThirdParty == nil || !ok {
		return nil, []string{"servidor sem as regras de terceiros"}, nil
	}
	list, err := cat.Approved(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range list {
		reason, err := e.ThirdParty.Ready(ctx, a.App, a.Version)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			notReady = append(notReady, a.App+" "+a.Version+" não pronto")
			continue
		}
		apps = append(apps, a)
	}
	return apps, notReady, nil
}

// issueApps cria um job por programa que a máquina tem em versão antiga. only: só esse programa
// (a nova tentativa de um adiado). skips: por que nenhum foi, quando nenhum foi.
func (e *Engine) issueApps(ctx context.Context, c *Campaign, id string, apps []AppVersion, only string, now time.Time) (int, []string, error) {
	created := 0
	reasons := map[string]bool{}
	for _, a := range apps {
		if only != "" && a.App != only {
			continue
		}
		p, skip, err := e.ThirdParty.Job(ctx, id, a.App, a.Version)
		if err != nil {
			return created, nil, err
		}
		if p == nil {
			switch skip {
			case "", "sem o programa":
			case "sem inventário do Windows", "arquitetura desconhecida":
				// Da máquina, não do programa (Linux, macOS, sem inventário): conta uma vez.
				return 0, []string{skip}, nil
			default:
				reasons[a.App+": "+skip] = true
			}
			continue
		}
		req := jobs.Request{MachineID: id, Type: jobs.TypeThirdParty, TTL: c.TTL, By: "campanha " + fmt.Sprint(c.ID),
			CampaignID: c.ID, CampaignIdx: c.CurrentIdx}
		if err := e.closeApps(ctx, c, id, p); err != nil {
			return created, nil, err
		}
		if req.Params, err = json.Marshal(p); err != nil {
			return created, nil, err
		}
		ok, skip, err := e.send(ctx, c, req, id, now)
		if err != nil {
			return created, nil, err
		}
		if c.State != StateRunning {
			return created, nil, nil
		}
		if ok {
			created++
		} else if skip != "" {
			reasons[skip] = true
		}
	}
	if created > 0 {
		return created, nil, nil
	}
	if len(reasons) == 0 {
		return 0, []string{"nenhum programa aprovado para atualizar"}, nil
	}
	return 0, slices.Sorted(maps.Keys(reasons)), nil
}
