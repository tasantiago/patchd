package agent

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Tentar assim que fechar: o job de terceiros adiado por programa aberto fica vigiado. Assim
// que nenhum dos executáveis do programa estiver rodando, o agente faz um check-in antecipado
// com o job em Unblocked, e a campanha pede de novo na hora (sem esperar a hora seguinte).
// Um segundo check-in, minutos depois, busca o job novo.

// ReasonProgramClosed: o motivo do check-in antecipado quando o programa fecha.
const ReasonProgramClosed = "programa-fechado"

const (
	blockedTick = time.Minute
	// blockedKeep: depois disso, o job deixa de ser vigiado (a campanha já tentou de novo
	// pelo intervalo, ou desistiu).
	blockedKeep = 24 * time.Hour
	// blockedFollowUp: o check-in que busca o job novo, depois do que avisou (a campanha
	// avança a cada minuto).
	blockedFollowUp = 2 * time.Minute
	maxBlocked      = protocol.MaxUnblocked
)

type blockedJob struct {
	names []string
	since time.Time
}

type blockedState struct {
	mu        sync.Mutex
	watch     map[int64]blockedJob
	unblocked []int64 // esperando o próximo check-in
}

func (a *Agent) blockedInit() *blockedState {
	a.blockedOnce.Do(func() { a.bs = &blockedState{watch: map[int64]blockedJob{}} })
	return a.bs
}

// watchBlocked passa a vigiar o job adiado por programa aberto.
func (a *Agent) watchBlocked(id int64, names []string) {
	if a.ProgramOpen == nil || len(names) == 0 {
		return
	}
	bs := a.blockedInit()
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if len(bs.watch) >= maxBlocked {
		return
	}
	bs.watch[id] = blockedJob{names: slices.Clone(names), since: a.now()}
}

// checkBlocked confere os jobs vigiados; true se algum programa fechou.
func (a *Agent) checkBlocked() bool {
	bs := a.blockedInit()
	bs.mu.Lock()
	defer bs.mu.Unlock()
	freed := false
	for id, b := range bs.watch {
		if a.now().Sub(b.since) > blockedKeep {
			delete(bs.watch, id)
			continue
		}
		open, err := a.ProgramOpen(b.names)
		if err != nil || open {
			continue // sem saber, ou ainda aberto: confere no minuto seguinte
		}
		delete(bs.watch, id)
		if !slices.Contains(bs.unblocked, id) && len(bs.unblocked) < maxBlocked {
			bs.unblocked = append(bs.unblocked, id)
		}
		freed = true
		a.Logger.Info("programa fechado: o job adiado pode ir de novo", "job_id", id, "processos", b.names)
	}
	return freed
}

// blockedLoop vigia os jobs adiados até o contexto acabar.
func (a *Agent) blockedLoop(ctx context.Context) {
	for {
		if err := a.sleep(ctx, blockedTick); err != nil {
			return
		}
		if a.checkBlocked() {
			a.wakeFor(ReasonProgramClosed)
		}
	}
}

// takeUnblocked: os jobs liberados, para o check-in (saem da lista só com o check-in aceito).
func (a *Agent) takeUnblocked() []int64 {
	bs := a.blockedInit()
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return slices.Clone(bs.unblocked)
}

// unblockedSent tira da lista os que o servidor recebeu e agenda o check-in que busca o
// job novo.
func (a *Agent) unblockedSent(sent []int64) {
	if len(sent) == 0 {
		return
	}
	bs := a.blockedInit()
	bs.mu.Lock()
	bs.unblocked = slices.DeleteFunc(bs.unblocked, func(id int64) bool { return slices.Contains(sent, id) })
	bs.mu.Unlock()
	time.AfterFunc(blockedFollowUp, func() { a.wakeFor(ReasonProgramClosed) })
}
