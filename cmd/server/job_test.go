package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
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
	if k, err := jobSigningKey(lookMap(nil)); k != nil || err != nil {
		t.Errorf("sem a variável, sem chave e sem erro (painel sem jobs): %v %v", k, err)
	}
	ruim := filepath.Join(dir, "ruim.key")
	_ = os.WriteFile(ruim, []byte("patchd_jobkey_curta"), 0o600)
	if _, err := jobSigningKey(lookMap(map[string]string{"PATCHD_JOB_SIGNING_KEY_FILE": ruim})); err == nil {
		t.Error("chave ilegível é erro")
	}
	// pubkey devolve a mesma chave pública do keygen, e o create sem chave explica o que fazer.
	var po, pe bytes.Buffer
	if code := runJob([]string{"pubkey"}, lookMap(map[string]string{"PATCHD_JOB_SIGNING_KEY_FILE": arq}), &po, &pe); code != exitOK ||
		strings.TrimSpace(po.String()) != strings.TrimSpace(out.String()) || !strings.Contains(pe.String(), jobs.KeyID(pub)) {
		t.Errorf("pubkey: %d %q %s", code, po.String(), pe.String())
	}
	if code := runJob([]string{"pubkey"}, lookMap(nil), &bytes.Buffer{}, &pe); code != exitConfig || !strings.Contains(pe.String(), "job keygen") {
		t.Errorf("pubkey sem chave: %d %s", code, pe.String())
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
		{[]string{"create", "-machine", "60ef", "-type", "thirdparty"}, exitConfig, "use package install"},
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

// Aula 16.1: o windows_update sai da busca atual da máquina; o KB vira o UpdateID dela.
func TestJobWindowsUpdatePorKB(t *testing.T) {
	_, key, _ := jobs.GenerateKey()
	pub := key.Public().(ed25519.PublicKey)
	ctx := context.Background()
	mem := store.NewMemory()
	const cu = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	for _, id := range []string{"60ef97e4-0001", "8bb71e2a-0003"} {
		_, _ = mem.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id})
	}
	_ = mem.SaveScan(ctx, "60ef97e4-0001", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
		Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{{ID: cu, KBs: []string{"5127648"}, Source: "wua-default"}}}}})
	st := memoriaComPrefixo{mem}
	agora := time.Now().UTC()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := jobCommand(ctx, st, key, args, agora, &o, &e)
		return code, o.String(), e.String()
	}
	code, o, e := roda("create", "-machine", "60ef", "-type", "windows_update", "-kb", "KB5127648, 5999999")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, "não mostra pendente: KB5999999") {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	env, _ := mem.DeliverJobs(ctx, "60ef97e4-0001", agora)
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", agora, 0)
	p, _ := jobs.ParseWinUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeWinUpdate || len(p.Updates) != 1 || p.Updates[0].ID != cu || p.Updates[0].KB != "5127648" {
		t.Errorf("job: %+v %+v %v", j, p, err)
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"create", "-machine", "60ef", "-type", "windows_update"}, exitConfig, "-kb vai com -type windows_update"},
		{[]string{"create", "-machine", "60ef", "-type", "rescan", "-kb", "5127648"}, exitConfig, "-kb vai com"},
		{[]string{"create", "-machine", "60ef", "-type", "windows_update", "-kb", "5999999"}, exitRuntime, "nenhum job criado"},
		{[]string{"create", "-machine", "8bb7", "-type", "windows_update", "-kb", "5127648"}, exitRuntime, "ainda não enviou busca"},
	} {
		if code, _, e := roda(c.args...); code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
}
