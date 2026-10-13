package campaign

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Operações do admin numa campanha (Aula 11.2: as mesmas da linha de comando e do painel).
const (
	OpPause  = "pause"
	OpResume = "resume"
	OpCancel = "cancel"
)

// ErrState: a operação não vale no estado atual (pausar uma pausada, cancelar uma concluída).
var ErrState = errors.New("operação fora de estado")

// ErrChanged: entre ler e gravar, a campanha mudou (o motor ou outro admin). Nada foi feito.
var ErrChanged = errors.New("a campanha mudou de estado agora há pouco; confira e tente de novo")

// ChangeStore é o que as operações usam do banco.
type ChangeStore interface {
	SaveCampaign(ctx context.Context, c Campaign, from string) (bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]JobState, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
}

// Change pausa, retoma ou cancela a campanha c (lida há pouco). A gravação só acontece se
// o estado ainda for o lido (from): o motor, que avança a cada minuto, nunca desfaz a
// decisão do admin, e o admin nunca desfaz a do motor sem ver. No cancelamento, os jobs
// ainda pendentes da campanha são cancelados; os entregues seguem (o agente pode estar
// executando). by aparece no detalhe ("pausada por fulano").
func Change(ctx context.Context, st ChangeStore, c Campaign, op, by string) (canceledJobs int, err error) {
	from := c.State
	switch op {
	case OpPause:
		if c.State != StateRunning {
			return 0, fmt.Errorf("%w: a campanha está %s; só se pausa uma em andamento", ErrState, c.State)
		}
		c.State, c.Detail = StatePaused, "pausada por "+by
	case OpResume:
		if err := Resume(&c); err != nil {
			return 0, fmt.Errorf("%w: %v", ErrState, err)
		}
		c.Detail = "retomada por " + by
	case OpCancel:
		if c.State == StateDone || c.State == StateCanceled {
			return 0, fmt.Errorf("%w: a campanha já está %s", ErrState, c.State)
		}
		c.State, c.Detail = StateCanceled, "cancelada por "+by
	default:
		return 0, fmt.Errorf("operação desconhecida %q", op)
	}
	ok, err := st.SaveCampaign(ctx, c, from)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrChanged
	}
	if op != OpCancel {
		return 0, nil
	}
	for idx := range c.Rings {
		list, err := st.CampaignJobs(ctx, c.ID, idx)
		if err != nil {
			return canceledJobs, err
		}
		for _, j := range list {
			if j.State == jobs.StatePending {
				if ok, _ := st.CancelJob(ctx, j.ID, "cancelamento da campanha "+strconv.FormatInt(c.ID, 10)); ok {
					canceledJobs++
				}
			}
		}
	}
	return canceledJobs, nil
}
