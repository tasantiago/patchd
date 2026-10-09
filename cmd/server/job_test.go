package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestJobKeygenECreate(t *testing.T) {
	dir := t.TempDir()
	arq := filepath.Join(dir, "jobs.key")
	var out, errOut bytes.Buffer
	if code := runJob([]string{"keygen", "-out", arq}, lookMap(nil), &out, &errOut); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errOut.String())
	}
	pub, err := jobs.ParsePublicKey(out.String())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(arq); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissão: %v", fi.Mode())
	}
	if code := runJob([]string{"keygen", "-out", arq}, lookMap(nil), &bytes.Buffer{}, &bytes.Buffer{}); code != exitRuntime {
		t.Error("não sobrescreve uma chave existente")
	}
	key, err := jobSigningKey(lookMap(map[string]string{"PATCHD_JOB_SIGNING_KEY_FILE": arq}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobSigningKey(lookMap(nil)); err == nil || !strings.Contains(err.Error(), "job keygen") {
		t.Errorf("sem chave: %v", err)
	}

	ctx := context.Background()
	mem := store.NewMemory()
	for _, id := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		_, _ = mem.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id})
	}
	_, _ = mem.RetireMachine(ctx, "7aa604f7-0002", "antiga", "teste")
	st := memoriaComPrefixo{mem}
	agora := time.Now().UTC()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := jobCommand(ctx, st, key, args, agora, &o, &e)
		return code, o.String(), e.String()
	}

	code, o, e := roda("create", "-machine", "60ef", "-type", "rescan")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, "job 1 (rescan) criado para a máquina 60ef97e4") {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	// O envelope gravado confere com a chave pública do keygen e é desta máquina.
	env, _ := mem.DeliverJobs(ctx, "60ef97e4-0001", agora)
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	if j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", agora, 0); err != nil || j.Type != jobs.TypeRescan || j.NotAfter.Sub(j.IssuedAt) != 24*time.Hour {
		t.Errorf("assinatura: %+v %v", j, err)
	}

	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"create", "-machine", "60ef", "-type", "shell"}, exitConfig, "-type"},
		{[]string{"create", "-machine", "60ef", "-type", "rescan", "-ttl", "500h"}, exitConfig, "-ttl"},
		{[]string{"create", "-machine", "7aa6", "-type", "rescan"}, exitRuntime, "aposentada"},
		{[]string{"create", "-machine", "ffff", "-type", "rescan"}, exitRuntime, "nenhuma máquina"},
		{[]string{"cancel", "-id", "1"}, exitRuntime, "não está pendente"},
		{[]string{"apagar"}, exitConfig, "desconhecido"},
	} {
		if code, _, e := roda(c.args...); code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	roda("create", "-machine", "60ef", "-type", "rescan")
	if code, _, e := roda("cancel", "-id", "2"); code != exitOK || !strings.Contains(e, "cancelado") {
		t.Errorf("cancel: %d %s", code, e)
	}
	_, o, _ = roda("list", "-machine", "60ef")
	if !strings.Contains(o, "2   60ef97e4  rescan  cancelado") || !strings.Contains(o, "1   60ef97e4  rescan  entregue") || !strings.Contains(o, "2 job(s)") {
		t.Errorf("list:\n%s", o)
	}
}
