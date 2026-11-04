package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/settings"
)

// O intervalo mandado pelo servidor vale no lugar do -checkin-interval, fica no estado
// (vale de novo depois de reiniciar) e some quando o servidor para de mandar.
func TestIntervaloDoServidor(t *testing.T) {
	log := &bytes.Buffer{}
	a := &Agent{Interval: time.Hour, DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(log, nil))}
	var st State
	if a.every() != time.Hour {
		t.Fatal("sem o servidor, vale o do agente")
	}
	if !a.applyServerInterval(15, &st) || a.every() != 15*time.Minute || st.ServerCheckinMinutes != 15 {
		t.Fatalf("15 min: %v %+v", a.every(), st)
	}
	if a.applyServerInterval(15, &st) {
		t.Error("o mesmo valor não muda o estado")
	}
	if err := saveState(a.DataDir, st); err != nil {
		t.Fatal(err)
	}
	// Reinício: o valor guardado vale antes do primeiro check-in.
	b := &Agent{Interval: time.Hour, DataDir: a.DataDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if s, err := loadState(b.DataDir); err != nil || s.ServerCheckinMinutes != 15 {
		t.Fatalf("estado: %+v %v", s, err)
	}
	// Fora dos limites ou ausente: volta ao do agente.
	if !a.applyServerInterval(1, &st) || a.every() != time.Hour || st.ServerCheckinMinutes != 0 {
		t.Fatalf("fora dos limites: %v %+v", a.every(), st)
	}
	if !bytes.Contains(log.Bytes(), []byte("intervalo de check-in mudou")) || !bytes.Contains(log.Bytes(), []byte("fora dos limites")) {
		t.Errorf("log:\n%s", log)
	}
}

type valores map[string]string

func (v valores) Value(k string) string  { return v[k] }
func (valores) Groups() []settings.Group { return nil }
func (valores) Apply(context.Context, map[string]string, string) ([]settings.Change, error) {
	return nil, nil
}
func (valores) Reset(context.Context, string, string) (settings.Change, bool, error) {
	return settings.Change{}, false, nil
}

// De ponta a ponta: o servidor com 15 min em Parâmetros; o check-in traz, o agente adota e
// guarda; com "0", volta ao dele.
func TestIntervaloDoServidorNoCheckin(t *testing.T) {
	p := valores{"PATCHD_CHECKIN_MINUTES": "15"}
	srv, id, cred := novoServidor(t, api.WithSettings(p))
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	a.MachineID, a.Interval = id, time.Hour
	a.CheckIn(context.Background())
	if a.every() != 15*time.Minute {
		t.Fatalf("depois do check-in: %v", a.every())
	}
	if s, _ := loadState(a.DataDir); s.ServerCheckinMinutes != 15 {
		t.Fatalf("estado: %+v", s)
	}
	p["PATCHD_CHECKIN_MINUTES"] = "0"
	a.CheckIn(context.Background())
	if a.every() != time.Hour {
		t.Fatalf("de volta ao do agente: %v", a.every())
	}
}
