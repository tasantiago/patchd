package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// maquinaWindows: o inventário muda quando a "instalação" muda o software.
type maquinaWindows struct {
	mu     sync.Mutex
	sw     []protocol.Software
	falhar atomic.Bool // a próxima coleta falha
}

func (m *maquinaWindows) inventario(context.Context) (protocol.InventoryReport, error) {
	if m.falhar.Swap(false) {
		return protocol.InventoryReport{}, errors.New("coleta falhou")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := inventarioDeTeste()
	rep.OS.ID = "windows"
	rep.Software = append([]protocol.Software(nil), m.sw...)
	h, err := rep.ContentHash()
	if err != nil {
		return rep, err
	}
	rep.Hash = h
	return rep, nil
}

func (m *maquinaWindows) troca(sw ...protocol.Software) {
	m.mu.Lock()
	m.sw = sw
	m.mu.Unlock()
}

var npp = func(v string) protocol.Software {
	return protocol.Software{Name: "Notepad++ (64-bit x64)", Version: v, Publisher: "Notepad++ Team", Scope: "machine"}
}

func paramsNpp() json.RawMessage {
	raw, _ := json.Marshal(jobs.ThirdPartyParams{App: "notepad-plus-plus", Version: "8.9.8.1", SHA256: strings.Repeat("26", 32),
		Size: 6963432, Installer: "nullsoft", Args: []string{"/S"}, ReturnCodes: map[string]string{"5": "packageInUse"}})
	return raw
}

func TestThirdPartyPorJob(t *testing.T) {
	m, _ := thirdparty.Default()
	srv, id, cred := novoServidor(t, api.WithThirdPartyManifest(&m))
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	pc := &maquinaWindows{sw: []protocol.Software{npp("8.9.7")}}
	a.Inventory = pc.inventario
	ctx := context.Background()
	if a.CheckIn(ctx); buscas.Load() != 1 {
		t.Fatal("primeiro ciclo")
	}
	novo := func() int64 {
		return criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, paramsNpp() })
	}

	// 1. Instala de verdade: o inventário de depois mostra 8.9.8.1 → concluído. Sem busca.
	var pedidos []jobs.ThirdPartyParams
	a.ThirdParty = func(_ context.Context, _ int64, p jobs.ThirdPartyParams) (string, error) {
		pedidos = append(pedidos, p)
		pc.troca(npp("8.9.8.1"))
		return "código 0 (ok) em 3s; assinatura válida: Notepad++ (O=Notepad++)", nil
	}
	j := novo()
	a.CheckIn(ctx)
	e := estadoDoJob(srv, id, j)
	if e.State != jobs.StateDone || !strings.Contains(e.Detail, "mostra Notepad++ (64-bit x64) 8.9.8.1 (exe); agente: código 0 (ok)") ||
		len(pedidos) != 1 || pedidos[0].Version != "8.9.8.1" || buscas.Load() != 1 {
		t.Fatalf("instalou: %+v (buscas %d)\n%s", e, buscas.Load(), log)
	}

	// 2. O instalador "funcionou", mas deixou uma segunda entrada visível → falhou.
	pc.troca(npp("8.9.7"))
	a.CheckIn(ctx)
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		msi := npp("8.9.8.1")
		msi.ProductCode = "{11111111-2222-3333-4444-555555555555}"
		pc.troca(npp("8.9.7"), msi)
		return "código 0 (ok)", nil
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "mostra 2 instalações visíveis de notepad-plus-plus (exe 8.9.7, msi 8.9.8.1)") {
		t.Errorf("duplicata: %+v", e)
	}

	// 3. "ok" sem mudar nada → falhou: o inventário não mudou depois da entrega.
	pc.troca(npp("8.9.7"))
	a.CheckIn(ctx)
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) { return "código 0 (ok)", nil }
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "o inventário não mudou depois da entrega") {
		t.Errorf("sem efeito: %+v", e)
	}

	// 4. Recusa do agente (hash): rejeitado, com o motivo.
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		return "", errors.Join(jobs.ErrRefused, errors.New("o arquivo recebido tem outro SHA-256"))
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "outro SHA-256") {
		t.Errorf("recusa: %+v", e)
	}

	// 5. O inventário de depois não sai neste ciclo: o resultado espera, e no próximo ciclo
	// o inventário vai primeiro e o job é conferido.
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		pc.troca(npp("8.9.8.1"))
		pc.falhar.Store(true) // a coleta logo depois da instalação falha
		return "código 0 (ok)", nil
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDelivered {
		t.Fatalf("o resultado devia esperar: %+v", e)
	}
	if st, _ := loadState(a.DataDir); !st.InventoryFirst || len(st.PendingJobResults) != 1 {
		t.Fatalf("estado: %+v", st)
	}
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone {
		t.Errorf("no ciclo seguinte: %+v\n%s", e, log)
	}
	if st, _ := loadState(a.DataDir); st.InventoryFirst || len(st.PendingJobResults) != 0 {
		t.Errorf("estado depois: %+v", st)
	}
}

// Sem instalador (Linux, macOS): o job é recusado sem tocar em nada.
func TestThirdPartyRecusadoForaDoWindows(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	j := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, paramsNpp() })
	a.CheckIn(context.Background())
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "só Windows") {
		t.Errorf("%+v", e)
	}
}

// O download falhou: o instalador não rodou, o job é "falhou" e o inventário não é reenviado.
func TestThirdPartySemDownloadNaoReenviaInventario(t *testing.T) {
	m, _ := thirdparty.Default()
	srv, id, cred := novoServidor(t, api.WithThirdPartyManifest(&m))
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	pc := &maquinaWindows{sw: []protocol.Software{npp("8.9.7")}}
	a.Inventory = pc.inventario
	ctx := context.Background()
	a.CheckIn(ctx)
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		return "", errors.Join(jobs.ErrNotStarted, errors.New("download do instalador: 404"))
	}
	antes := srv.conta("/api/v1/agent/inventory")
	j := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, paramsNpp() })
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "404") {
		t.Errorf("job: %+v", e)
	}
	if n := srv.conta("/api/v1/agent/inventory"); n != antes {
		t.Errorf("o inventário foi reenviado sem instalação: %d → %d", antes, n)
	}
}

// Aula 14.9: programa aberto (tentar depois, sem rodar) vira "adiado", não "falhou", e o
// inventário não é reenviado.
func TestThirdPartyAdiado(t *testing.T) {
	m, _ := thirdparty.Default()
	srv, id, cred := novoServidor(t, api.WithThirdPartyManifest(&m))
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	pc := &maquinaWindows{sw: []protocol.Software{npp("8.9.7")}}
	a.Inventory = pc.inventario
	ctx := context.Background()
	a.CheckIn(ctx)
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		return "", fmt.Errorf("%w: %w: programa aberto (2 notepad++.exe na sessão 1)", jobs.ErrNotStarted, jobs.ErrRetry)
	}
	antes := srv.conta("/api/v1/agent/inventory")
	j := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, paramsNpp() })
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StatePostponed || !strings.Contains(e.Detail, "adiado pelo agente: o instalador não chegou a rodar: tentar depois: programa aberto") {
		t.Errorf("job: %+v", e)
	}
	if n := srv.conta("/api/v1/agent/inventory"); n != antes {
		t.Errorf("o inventário foi reenviado sem instalação: %d → %d", antes, n)
	}
}
