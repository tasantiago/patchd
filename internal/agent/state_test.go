package agent

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Rede e discos (Aula 12.1) vão em todo check-in, sem envio à parte; uma falha parcial da
// coleta vai junto (em errors) e aparece no log do agente.
func TestCheckinLevaEstado(t *testing.T) {
	srv, id, cred := novoServidor(t)
	var buscas atomic.Int32
	var log bytes.Buffer
	a := agenteDeTeste(t, srv, cred, &buscas, &log)
	coletas := 0
	a.State = func(context.Context) protocol.MachineState {
		coletas++
		return protocol.MachineState{CollectedAt: time.Now().UTC(),
			Interfaces: []protocol.NetInterface{{Name: "eth0", IPv4: []string{"192.0.2.1/24"}, Up: true}},
			Disks:      []protocol.Disk{{Mount: "C:", FS: "NTFS", TotalBytes: 100, FreeBytes: 10}},
			Errors:     []string{"discos: D: volume bloqueado"}}
	}
	ctx := context.Background()
	for range 2 {
		if out := a.CheckIn(ctx); out != outcomeOK {
			t.Fatalf("ciclo: %v", out)
		}
	}
	if coletas != 2 || srv.conta("/api/v1/agent/checkin") != 2 {
		t.Errorf("uma coleta por check-in: coletas=%d checkins=%d", coletas, srv.conta("/api/v1/agent/checkin"))
	}
	h, _, _ := srv.st.HostState(ctx, id)
	if h.State == nil || len(h.State.Disks) != 1 || h.State.Disks[0].Mount != "C:" || h.CheckinAddr != "127.0.0.1" {
		t.Errorf("estado no servidor: %+v", h)
	}
	if !strings.Contains(log.String(), "coleta de rede e discos incompleta") {
		t.Errorf("falha parcial no log:\n%s", log.String())
	}

	// Sem coletor (State nil), o check-in vai como antes.
	a.State = nil
	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("sem coletor: %v", out)
	}
}
