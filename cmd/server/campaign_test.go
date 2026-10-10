package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestComandosDeCampanha(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	for maq, anel := range map[string]int{"60ef97e4-0001": 0, "92fc3b93-0002": 1} {
		_, _ = mem.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: maq})
		_, _ = mem.SetMachineRing(ctx, maq, anel)
	}
	agora := time.Now().UTC()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, args, agora, &o, &e)
		return code, o.String(), e.String()
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"create", "-name", "x", "-type", "rescan", "-rings", "1,0"}, "ordem"},
		{[]string{"create", "-name", "x", "-type", "update", "-rings", "0,1"}, "fontes"},
		{[]string{"create", "-type", "rescan", "-rings", "0"}, "nome"},
		{[]string{"create", "-name", "x", "-type", "shell", "-rings", "0"}, "desconhecido"},
	} {
		if code, _, e := roda(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	code, o, e := roda("create", "-name", "busca geral", "-type", "rescan", "-rings", "0,1", "-observe", "10m", "-ttl", "15m")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, "libera o anel 0 no próximo minuto") {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	// O laço do servidor libera o anel 0.
	_, key, _ := jobs.GenerateKey()
	eng := &campaign.Engine{Store: mem, Key: key}
	if err := eng.Step(ctx, agora); err != nil {
		t.Fatal(err)
	}
	_, o, _ = roda("show", "-id", "1")
	if !strings.Contains(o, "0 (atual)  1    60ef97e4  pendente") || !strings.Contains(o, "ainda não liberado") || !strings.Contains(o, "observação: 10m0s") {
		t.Errorf("show:\n%s", o)
	}
	if _, o, _ := roda("list"); !strings.Contains(o, "[0]→1") || !strings.Contains(o, "em_andamento") {
		t.Errorf("list:\n%s", o)
	}
	if code, _, e := roda("resume", "-id", "1"); code != exitRuntime || !strings.Contains(e, "não pausada") {
		t.Errorf("resume em andamento: %d %s", code, e)
	}
	if code, _, e := roda("pause", "-id", "1"); code != exitOK || !strings.Contains(e, "pausada") {
		t.Errorf("pause: %d %s", code, e)
	}
	if code, _, _ := roda("resume", "-id", "1"); code != exitOK {
		t.Error("resume")
	}
	code, _, e = roda("cancel", "-id", "1")
	if code != exitOK || !strings.Contains(e, "1 job(s) pendente(s) cancelado(s)") {
		t.Errorf("cancel: %d %s", code, e)
	}
	if l, _ := mem.Jobs(ctx, "60ef97e4-0001", 1); l[0].State != jobs.StateCanceled || !strings.Contains(l[0].Detail, "campanha 1") {
		t.Errorf("job cancelado: %+v", l[0])
	}
	if code, _, _ := roda("cancel", "-id", "1"); code != exitRuntime {
		t.Error("cancelar de novo")
	}
	if code, _, e := roda("show", "-id", "9"); code != exitRuntime || !strings.Contains(e, "não existe") {
		t.Errorf("inexistente: %s", e)
	}
}
