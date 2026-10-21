package api

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/store"
)

func TestDesfechoDaGravacao(t *testing.T) {
	entregue := time.Now()
	for nome, c := range map[string]struct {
		j     store.JobRow
		quer  string
		final bool
	}{
		"pendente":                    {store.JobRow{State: jobs.StatePending}, "", false},
		"entregue":                    {store.JobRow{State: jobs.StateDelivered, DeliveredAt: &entregue}, "", false},
		"concluído":                   {store.JobRow{State: jobs.StateDone}, store.BIOSActive, true},
		"falha clara":                 {store.JobRow{State: jobs.StateFailed, Detail: "o agente informou falha: hp respondeu 6"}, store.BIOSFailed, true},
		"falha incerta":               {store.JobRow{State: jobs.StateFailed, Detail: "o agente informou falha: " + jobs.BIOSUncertain + "x"}, store.BIOSUncertain, true},
		"recusado":                    {store.JobRow{State: jobs.StateRejected, Detail: "Sure Admin"}, store.BIOSPending, true},
		"expirado sem entrega":        {store.JobRow{State: jobs.StateExpired}, store.BIOSPending, true},
		"expirado depois da entrega":  {store.JobRow{State: jobs.StateExpired, DeliveredAt: &entregue}, store.BIOSUncertain, true},
		"cancelado depois da entrega": {store.JobRow{State: jobs.StateCanceled, DeliveredAt: &entregue}, store.BIOSUncertain, true},
		"cancelado antes":             {store.JobRow{State: jobs.StateCanceled}, store.BIOSPending, true},
	} {
		got, _, final := biosOutcome(c.j)
		if got != c.quer || final != c.final {
			t.Errorf("%s: %q %v", nome, got, final)
		}
	}
}
