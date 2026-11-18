package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O job desinstalar: o agente remove e manda o inventário antes do resultado; o servidor só
// conclui se a entrada saiu do inventário.
func TestDesinstalarPorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	chave := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Programa`
	prog := protocol.Software{Name: "Programa", Version: "1.0", Scope: "machine", Uninstall: "quiet", Source: chave}
	pc := &maquinaWindows{sw: []protocol.Software{prog, npp("8.9.7")}}
	a.Inventory = pc.inventario
	ctx := context.Background()
	a.CheckIn(ctx)
	params, _ := json.Marshal(jobs.UninstallParams{ReplaceEntry: jobs.ReplaceEntry{Name: "Programa", Version: "1.0", Key: chave, Method: "quiet"}})
	novo := func() int64 {
		return criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeUninstall, params })
	}

	// Sem a remoção (Linux, macOS): recusado.
	j := novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "só Windows") {
		t.Fatalf("sem remoção: %+v", e)
	}
	// Removeu de verdade: concluído.
	var pedidos []jobs.UninstallParams
	a.Uninstall = func(_ context.Context, _ int64, p jobs.UninstallParams) (string, error) {
		pedidos = append(pedidos, p)
		pc.troca(npp("8.9.7"))
		return "Programa 1.0 removido", nil
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone || !strings.Contains(e.Detail, "não mostra mais Programa 1.0") || len(pedidos) != 1 {
		t.Fatalf("removeu: %+v\n%s", e, log)
	}
	// "ok" com a entrada ainda lá: falhou.
	pc.troca(prog, npp("8.9.7"))
	a.CheckIn(ctx)
	a.Uninstall = func(context.Context, int64, jobs.UninstallParams) (string, error) { return "ok", nil }
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "ainda mostra Programa 1.0") {
		t.Errorf("ainda lá: %+v", e)
	}
	// Outro programa na chave: recusado pelo agente.
	a.Uninstall = func(context.Context, int64, jobs.UninstallParams) (string, error) {
		return "", errors.Join(jobs.ErrRefused, errors.New("a entrada agora é de outro programa"))
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "outro programa") {
		t.Errorf("recusa: %+v", e)
	}
}
