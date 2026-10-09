package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestAneisJanelasEJobNaJanela(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	for _, id := range []string{"60ef97e4-0001", "92fc3b93-0002", "aaaa0000-0003"} {
		_, _ = mem.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id})
	}
	st := memoriaComPrefixo{mem}
	pvh, _ := time.LoadLocation("America/Porto_Velho")
	agora := time.Date(2026, 10, 9, 10, 0, 0, 0, pvh).UTC() // sexta, 10:00 em Porto Velho
	anel := func(tz string, args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := ringCommand(ctx, st, args, tz, agora, &o, &e)
		return code, o.String(), e.String()
	}

	if code, _, e := anel("", "set", "-machine", "60ef,92fc", "-ring", "0"); code != exitOK || !strings.Contains(e, "máquina 60ef97e4 no anel 0 (piloto)") ||
		!strings.Contains(e, "máquina 92fc3b93 no anel 0") {
		t.Fatalf("set: %d %s", code, e)
	}
	if code, _, e := anel("", "set", "-machine", "60ef", "-ring", "10"); code != exitConfig || !strings.Contains(e, "-ring") {
		t.Errorf("anel 10: %d %s", code, e)
	}
	if code, _, e := anel("", "window", "-ring", "0", "-days", "seg-sex", "-start", "12:00", "-duration", "2h"); code != exitConfig || !strings.Contains(e, "PATCHD_TIMEZONE") {
		t.Errorf("sem fuso: %d %s", code, e)
	}
	code, _, e := anel("America/Porto_Velho", "window", "-ring", "0", "-days", "seg-sex", "-start", "12:00", "-duration", "2h")
	if code != exitOK || !strings.Contains(e, "seg-sex 12:00-14:00 (America/Porto_Velho); a próxima vai de 09/10 12:00 a 09/10 14:00") {
		t.Fatalf("window: %d %s", code, e)
	}
	for _, ruim := range [][]string{
		{"window", "-ring", "0", "-days", "sex-seg", "-start", "12:00", "-duration", "2h"},
		{"window", "-ring", "0", "-days", "seg", "-start", "25:00", "-duration", "2h"},
		{"window", "-ring", "0", "-days", "seg", "-start", "12:00", "-duration", "5m"},
		{"window", "-ring", "0", "-days", "seg", "-start", "12:00", "-duration", "2h", "-tz", "Marte/Olimpo"},
	} {
		if code, _, _ := anel("America/Porto_Velho", ruim...); code != exitConfig {
			t.Errorf("%v: %d", ruim, code)
		}
	}
	_, o, _ := anel("", "list")
	if !strings.Contains(o, "0 (piloto)  2") || !strings.Contains(o, "09/10 12:00") || !strings.Contains(o, "1           1         sem janela") ||
		!strings.Contains(o, "3 máquina(s)") {
		t.Errorf("list:\n%s", o)
	}

	// Jobs na janela.
	pub, key, _ := jobs.GenerateKey()
	job := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := jobCommand(ctx, st, key, args, agora, &o, &e)
		return code, o.String(), e.String()
	}
	code, o, e = job("create", "-machine", "60ef", "-type", "rescan", "-window")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, "na janela do anel 0, de 09/10 12:00 a 09/10 14:00 (America/Porto_Velho)") {
		t.Fatalf("create -window: %d %q %s", code, o, e)
	}
	if env, _ := mem.DeliverJobs(ctx, "60ef97e4-0001", agora); len(env) != 0 {
		t.Error("entregue antes da janela")
	}
	env, _ := mem.DeliverJobs(ctx, "60ef97e4-0001", agora.Add(2*time.Hour+time.Minute))
	if len(env) != 1 {
		t.Fatal("não entregue na janela")
	}
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", agora.Add(2*time.Hour+time.Minute), 0)
	if err != nil || !j.NotBefore.Equal(agora.Add(2*time.Hour)) || !j.NotAfter.Equal(agora.Add(4*time.Hour)) {
		t.Errorf("payload: %+v %v", j, err)
	}

	// Um para cada máquina do anel; anel sem janela; combinações inválidas.
	code, o, e = job("create", "-ring", "0", "-type", "rescan", "-window")
	if code != exitOK || strings.Count(o, "\n") != 2 || !strings.Contains(e, "máquina 92fc3b93 na janela") {
		t.Errorf("create -ring: %d %q %s", code, o, e)
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"create", "-ring", "1", "-type", "rescan", "-window"}, exitRuntime, "não tem janela"},
		{[]string{"create", "-ring", "5", "-type", "rescan"}, exitRuntime, "não tem máquinas"},
		{[]string{"create", "-machine", "60ef", "-ring", "0", "-type", "rescan"}, exitConfig, "um dos dois"},
		{[]string{"create", "-machine", "60ef", "-type", "rescan", "-window", "-ttl", "1h"}, exitConfig, "não combinam"},
	} {
		if code, _, e := job(c.args...); code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	if _, o, _ := job("list", "-machine", "92fc"); !strings.Contains(o, "abre em 2 h 00 min") {
		t.Errorf("list com janela:\n%s", o)
	}
}

func TestJobUpdatePelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	_, _ = mem.SaveInventory(ctx, "60ef97e4-0001", protocol.InventoryReport{SchemaVersion: 1, Hash: "x"})
	st := memoriaComPrefixo{mem}
	pub, key, _ := jobs.GenerateKey()
	agora := time.Now().UTC()
	job := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := jobCommand(ctx, st, key, args, agora, &o, &e)
		return code, o.String(), e.String()
	}
	code, o, e := job("create", "-machine", "60ef", "-type", "update", "-packages", "curl=8.18.0-1ubuntu2.10, libcurl4t64=8.18.0-1ubuntu2.10")
	if code != exitOK || strings.TrimSpace(o) != "1" {
		t.Fatalf("update: %d %q %s", code, o, e)
	}
	env, _ := mem.DeliverJobs(ctx, "60ef97e4-0001", agora)
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", agora, 0)
	p, _ := jobs.ParseUpdateParams(j.Params)
	if err != nil || len(p.Packages) != 2 || p.Packages[1].Name != "libcurl4t64" {
		t.Errorf("payload: %+v %v", p, err)
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"create", "-machine", "60ef", "-type", "update"}, "-packages"},
		{[]string{"create", "-machine", "60ef", "-type", "rescan", "-packages", "curl=1"}, "-packages"},
		{[]string{"create", "-machine", "60ef", "-type", "update", "-packages", "curl"}, "sem a versão"},
		{[]string{"create", "-machine", "60ef", "-type", "update", "-packages", "-o=1"}, "inválido"},
	} {
		if code, _, e := job(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
}
