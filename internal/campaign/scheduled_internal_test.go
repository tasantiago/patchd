package campaign

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

// O job adiado porque o servidor só baixa o arquivo da Microsoft no horário: a nova tentativa
// espera o horário abrir (mais a folga), e não a hora de sempre; o detalhe vem do agente, e a
// espera tem teto.
func TestAdiadoPeloHorarioDoCache(t *testing.T) {
	fim := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	abre := time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC) // 22:00 em Porto Velho
	detalhe := "adiado pelo agente: pelo cache do servidor: tentar depois: 0123456789abcdef-a.msu: " + ScheduledMark + ", a partir de 16/11 22:00 [2026-11-17T02:00:00Z]"
	lista := []JobState{
		{ID: 1, MachineID: "agendada", State: jobs.StatePostponed, Detail: detalhe, FinishedAt: &fim},
		{ID: 2, MachineID: "comum", State: jobs.StatePostponed, Detail: "adiado pelo agente: tentar depois: programa aberto", FinishedAt: &fim},
		// Um agente que tenta se adiar por um ano: vale o teto de 26 h.
		{ID: 3, MachineID: "esperta", State: jobs.StatePostponed, Detail: ScheduledMark + " [2027-11-17T02:00:00Z]", FinishedAt: &fim},
		// O texto sem a hora, ou a hora sem o texto: o intervalo de sempre.
		{ID: 4, MachineID: "sem-hora", State: jobs.StatePostponed, Detail: ScheduledMark, FinishedAt: &fim},
		{ID: 5, MachineID: "sem-marca", State: jobs.StatePostponed, Detail: "qualquer coisa [2026-11-17T02:00:00Z]", FinishedAt: &fim},
	}
	quem := func(now time.Time) map[string]bool {
		out := map[string]bool{}
		for _, j := range ringStatus(lista, now, 0).retry {
			out[j.MachineID] = true
		}
		return out
	}
	// Uma hora e meia depois: as de sempre tentam; as agendadas esperam.
	r := quem(fim.Add(90 * time.Minute))
	if !r["comum"] || !r["sem-hora"] || !r["sem-marca"] || r["agendada"] || r["esperta"] {
		t.Errorf("antes do horário: %v", r)
	}
	if st := ringStatus(lista, fim.Add(90*time.Minute), 0); st.waiting != 2 || !st.next.Equal(abre.Add(scheduledSlack)) {
		t.Errorf("esperando %d, próxima %v", st.waiting, st.next)
	}
	// O horário abriu, mas a folga não passou.
	if r := quem(abre.Add(10 * time.Minute)); r["agendada"] {
		t.Error("tentou antes da folga")
	}
	if r := quem(abre.Add(scheduledSlack)); !r["agendada"] || r["esperta"] {
		t.Errorf("depois da folga: %v", r)
	}
	if r := quem(fim.Add(26 * time.Hour)); !r["esperta"] {
		t.Error("o teto de 26 h não valeu")
	}
}
