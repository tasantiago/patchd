package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// biosDeTeste: a máquina com a BIOS que o job muda; o inventário mostra o firmware.
type biosDeTeste struct {
	mu    sync.Mutex
	senha bool
}

func (b *biosDeTeste) inventario(context.Context) (protocol.InventoryReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rep := inventarioDeTeste()
	rep.OS.ID = "windows"
	s := b.senha
	rep.Hardware = &protocol.Hardware{Manufacturer: "HP", Sources: []string{}, Firmware: &protocol.Firmware{Interface: "hp", AdminPassword: &s}}
	h, err := rep.ContentHash()
	rep.Hash = h
	return rep, err
}

func (b *biosDeTeste) define(v bool) {
	b.mu.Lock()
	b.senha = v
	b.mu.Unlock()
}

// TestSenhaDaBIOSPorJob (Aula 21.5): o resultado vai depois do inventário de depois; o
// servidor confere a senha definida nele; recusa vira "rejeitado"; incerto, "falhou" com o
// detalhe que a guarda reconhece.
func TestSenhaDaBIOSPorJob(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := agenteDeTeste(t, srv, cred, &buscas, log)
	a.MachineID, a.JobKey = id, pub
	bios := &biosDeTeste{}
	a.Inventory = bios.inventario
	ctx := context.Background()
	a.CheckIn(ctx)
	env := base64.StdEncoding.EncodeToString([]byte("envelope"))
	novo := func() int64 {
		raw, _ := jobs.BIOSPasswordParamsJSON(jobs.BIOSPasswordParams{Guard: 1, Vendor: "hp", BoxKey: "0123456789ab", New: env})
		return criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeBIOSPassword, raw })
	}

	// Sem o executor (Linux, macOS): recusado.
	j := novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateRejected || !strings.Contains(e.Detail, "não grava a senha da BIOS") {
		t.Fatalf("sem executor: %+v", e)
	}

	// Gravou: o inventário de depois mostra a senha → concluído.
	var pedidos []jobs.BIOSPasswordParams
	a.BIOSPassword = func(_ context.Context, _ int64, p jobs.BIOSPasswordParams) (string, error) {
		pedidos = append(pedidos, p)
		bios.define(true)
		return "senha de administrador da BIOS definida (hp, código 0)", nil
	}
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateDone || !strings.Contains(e.Detail, "o inventário mostra a senha de administrador definida; agente: senha de administrador da BIOS definida") ||
		len(pedidos) != 1 || pedidos[0].Guard != 1 {
		t.Fatalf("gravou: %+v\n%s", e, log)
	}

	// "ok" sem a senha no inventário: incerto (o servidor não confirma).
	bios.define(false)
	a.CheckIn(ctx)
	a.BIOSPassword = func(context.Context, int64, jobs.BIOSPasswordParams) (string, error) { return "código 0", nil }
	j = novo()
	a.CheckIn(ctx)
	if e := estadoDoJob(srv, id, j); e.State != jobs.StateFailed || !strings.Contains(e.Detail, jobs.BIOSUncertain+"o agente informou sucesso, mas o inventário não mostra") {
		t.Errorf("sem confirmação: %+v", e)
	}

	// Recusa (a BIOS não foi tocada), falha clara e incerta.
	for _, c := range []struct {
		err    error
		estado string
		quer   string
	}{
		{fmt.Errorf("%w: HP Sure Admin ligado; a BIOS não foi tocada", jobs.ErrRefused), jobs.StateRejected, "HP Sure Admin ligado"},
		{errors.New("hp respondeu 6 (acesso negado)"), jobs.StateFailed, "o agente informou falha: hp respondeu 6"},
		{fmt.Errorf("%w: a chamada quebrou", jobs.ErrBIOSUncertain), jobs.StateFailed, jobs.BIOSUncertain},
	} {
		a.BIOSPassword = func(context.Context, int64, jobs.BIOSPasswordParams) (string, error) { return "", c.err }
		j = novo()
		a.CheckIn(ctx)
		if e := estadoDoJob(srv, id, j); e.State != c.estado || !strings.Contains(e.Detail, c.quer) {
			t.Errorf("%v: %+v", c.err, e)
		}
	}
}
