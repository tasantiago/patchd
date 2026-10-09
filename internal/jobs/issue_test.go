package jobs_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

type filaFalsa struct {
	seq   int64
	gravs []jobs.Record
	err   error
}

func (f *filaFalsa) NextJobID(context.Context) (int64, error) { f.seq++; return f.seq, nil }
func (f *filaFalsa) CreateJob(_ context.Context, r jobs.Record) error {
	if f.err != nil {
		return f.err
	}
	f.gravs = append(f.gravs, r)
	return nil
}

func TestEmitir(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	agora := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ok := jobs.Request{MachineID: "maq-a", Type: jobs.TypeRescan, TTL: 3 * time.Hour, By: "thyago"}
	f := &filaFalsa{}
	rec, err := jobs.Issue(context.Background(), f, priv, ok, agora)
	if err != nil || rec.ID != 1 || len(f.gravs) != 1 || rec.CreatedBy != "thyago" || !rec.NotAfter.Equal(agora.Add(3*time.Hour)) {
		t.Fatalf("emitir: %+v %v", rec, err)
	}
	if j, err := jobs.Verify(pub, rec.Signed, "maq-a", agora, 0); err != nil || j.ID != 1 {
		t.Errorf("o que foi gravado confere: %+v %v", j, err)
	}

	for nome, c := range map[string]struct {
		key []byte
		req jobs.Request
	}{
		"sem chave":   {nil, ok},
		"tipo":        {priv, jobs.Request{MachineID: "maq-a", Type: "shell", TTL: time.Hour, By: "x"}},
		"prazo curto": {priv, jobs.Request{MachineID: "maq-a", Type: jobs.TypeRescan, TTL: time.Minute, By: "x"}},
		"prazo longo": {priv, jobs.Request{MachineID: "maq-a", Type: jobs.TypeRescan, TTL: 8 * 24 * time.Hour, By: "x"}},
		"sem autor":   {priv, jobs.Request{MachineID: "maq-a", Type: jobs.TypeRescan, TTL: time.Hour}},
		"sem máquina": {priv, jobs.Request{Type: jobs.TypeRescan, TTL: time.Hour, By: "x"}},
	} {
		f := &filaFalsa{}
		if _, err := jobs.Issue(context.Background(), f, c.key, c.req, agora); err == nil || f.seq != 0 {
			t.Errorf("%s: aceito (%v), ou reservou ID antes de validar", nome, err)
		}
	}
	f = &filaFalsa{err: jobs.ErrMachineUnavailable}
	if _, err := jobs.Issue(context.Background(), f, priv, ok, agora); !errors.Is(err, jobs.ErrMachineUnavailable) {
		t.Errorf("aposentada: %v", err)
	}

	id := jobs.KeyID(pub)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) || id != jobs.KeyID(pub) {
		t.Errorf("key_id: %q", id)
	}
	if outra, _, _ := jobs.GenerateKey(); jobs.KeyID(outra) == id {
		t.Error("chaves diferentes, impressões diferentes")
	}
}
