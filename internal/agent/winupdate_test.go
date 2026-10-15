package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

const guidKB = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"

// buscaDoWindows: o que a busca do WUA mostra, trocado pela "instalação".
type buscaDoWindows struct {
	mu         sync.Mutex
	pendente   bool // o KB ainda aparece na busca
	reiniciar  bool // reinício pendente
	quantasVez atomic.Int32
}

func (b *buscaDoWindows) busca(context.Context) protocol.PatchScanReport {
	b.quantasVez.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	missing := []protocol.MissingUpdate{}
	if b.pendente {
		missing = append(missing, protocol.MissingUpdate{ID: guidKB, KBs: []string{"5127648"}, Title: "Atualização cumulativa",
			Type: "software", Source: "wua-default"})
	}
	pend := b.reiniciar
	return protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
		Scans:  []protocol.ScanResult{{Source: "wua-default", Missing: missing}},
		Reboot: &protocol.RebootStatus{Pending: &pend}}
}

func (b *buscaDoWindows) muda(pendente, reiniciar bool) {
	b.mu.Lock()
	b.pendente, b.reiniciar = pendente, reiniciar
	b.mu.Unlock()
}

func TestWindowsUpdatePorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	b := &buscaDoWindows{pendente: true}
	a.Scan = b.busca
	ctx := context.Background()
	a.CheckIn(ctx)
	novo := func() int64 {
		raw, _ := json.Marshal(jobs.WinUpdateParams{Updates: []jobs.WinUpdate{{ID: guidKB, KB: "5127648"}}})
		return criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeWinUpdate, raw })
	}

	// 1. Sem executor (Linux, macOS): recusado, dizendo por quê, sem busca a mais.
	j := novo()
	antes := b.quantasVez.Load()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "só o agente do Windows") || b.quantasVez.Load() != antes {
		t.Fatalf("sem executor: %+v", e)
	}

	// 2. Instala e o KB some da busca de depois → concluído. A busca é forçada pelo job.
	var pedidos [][]jobs.WinUpdate
	a.WinUpdate = func(_ context.Context, _ int64, u []jobs.WinUpdate) (string, error) {
		pedidos = append(pedidos, u)
		b.muda(false, false)
		return "instalada(s): KB5127648; sem reinício", nil
	}
	j = novo()
	antes = b.quantasVez.Load()
	a.CheckIn(ctx)
	e := estadoDoJob(srv, id, j)
	if e.State != jobs.StateDone || !strings.Contains(e.Detail, "não mostra mais KB5127648; agente: instalada(s): KB5127648") ||
		len(pedidos) != 1 || pedidos[0][0].ID != guidKB || b.quantasVez.Load() != antes+1 {
		t.Fatalf("instalou: %+v\n%s", e, log)
	}

	// 3. Instalada, mas o Windows só a dá por instalada depois do boot → concluído, com o aviso.
	b.muda(true, false)
	a.WinUpdate = func(context.Context, int64, []jobs.WinUpdate) (string, error) {
		b.muda(true, true)
		return "instalada(s): KB5127648; o Windows pede reinício para concluir (peça pelo painel: Reiniciar)", nil
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone || !strings.Contains(e.Detail, "aguardando reinício") {
		t.Errorf("reinício pendente: %+v", e)
	}

	// 4. "ok" sem efeito e sem reinício pendente → falhou.
	b.muda(true, false)
	a.WinUpdate = func(context.Context, int64, []jobs.WinUpdate) (string, error) { return "instalada(s): KB5127648", nil }
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "ainda mostra pendente: KB5127648, sem reinício pendente") {
		t.Errorf("sem efeito: %+v", e)
	}

	// 5. A fonte da máquina não oferece (WSUS sem aprovação) → recusado; ocupado → adiado.
	a.WinUpdate = func(context.Context, int64, []jobs.WinUpdate) (string, error) {
		return "", errors.Join(jobs.ErrNotPending, errors.New("KB5127648 não é oferecida a esta máquina"))
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "não é oferecida") {
		t.Errorf("não oferecida: %+v", e)
	}
	a.WinUpdate = func(context.Context, int64, []jobs.WinUpdate) (string, error) {
		return "", errors.Join(jobs.ErrRetry, errors.New("outra instalação do Windows Update em andamento"))
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StatePostponed || !strings.Contains(e.Detail, "outra instalação") {
		t.Errorf("ocupado: %+v", e)
	}
}
