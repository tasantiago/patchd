package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

func envDeTeste(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// Aula 19.2: a tela confere cada parâmetro com a mesma regra da partida.
func TestParametrosConferidosComoNaPartida(t *testing.T) {
	defs := map[string]settings.Def{}
	for _, d := range settingDefs(envDeTeste(map[string]string{"PATCHD_REPO_CACHE_DIR": "/var/cache/patchd"})) {
		defs[d.Key] = d
	}
	for _, c := range []struct {
		key, in, out string
		ok           bool
	}{
		{"PATCHD_PANEL_TITLE", `"patchd · TJ"`, "patchd · TJ", true},
		{"PATCHD_PANEL_TITLE", strings.Repeat("x", 41), "", false},
		{"PATCHD_APP_NAME", ` "Atualizações da TI" `, "Atualizações da TI", true}, // Aula 20.10
		{"PATCHD_APP_NAME", strings.Repeat("x", 41), "", false},
		{"PATCHD_APP_NAME", "", "", false},
		{"PATCHD_APP_NAME", "Central\u200b", "", false},
		{"PATCHD_TIMEZONE", " America/Porto_Velho ", "America/Porto_Velho", true},
		{"PATCHD_TIMEZONE", "", "", true},
		{"PATCHD_TIMEZONE", "Marte/Olympus", "", false},
		{"PATCHD_TIMEZONE", "Local", "", false},
		{"PATCHD_RETENTION_HISTORY_DAYS", " 120", "120", true},
		{"PATCHD_RETENTION_HISTORY_DAYS", "0", "", false},
		{"PATCHD_RETENTION_JOBS_DAYS", "0", "0", true},
		{"PATCHD_RETENTION_JOBS_DAYS", "30", "", false},
		{"PATCHD_RETENTION_AUDIT_DAYS", "200", "", false},
		{"PATCHD_CATALOG_INTERVAL", "6h", "6h", true},
		{"PATCHD_CATALOG_INTERVAL", "5m", "", false},
		{"PATCHD_REPO_CACHE_URL", "http://cache.exemplo:8080", "http://cache.exemplo:8080", true},
		{"PATCHD_REPO_CACHE_URL", "ftp://x", "", false},
		{"PATCHD_REPO_CACHE_URL", "", "", true},
	} {
		got, err := defs[c.key].Check(c.in)
		if (err == nil) != c.ok || got != c.out {
			t.Errorf("%s=%q: %q %v", c.key, c.in, got, err)
		}
	}
	if why := defs["PATCHD_REPO_CACHE_URL"].Locked(); why != "" {
		t.Errorf("com o diretório, a URL muda pela tela: %q", why)
	}
	for _, d := range settingDefs(envDeTeste(nil)) {
		if d.Key == "PATCHD_REPO_CACHE_URL" && d.Locked() == "" {
			t.Error("sem PATCHD_REPO_CACHE_DIR, a URL fica travada")
		}
		if strings.Contains(d.Key, "PASSWORD") || strings.Contains(d.Key, "SECRET") || strings.Contains(d.Key, "DATABASE") {
			t.Errorf("%s não pode ir para a tela", d.Key)
		}
	}
	if showDays("0") != "0 (guardar sempre)" || showDays("90") != "90 dias" {
		t.Error("exibição dos dias")
	}
}

// O comando settings: list mostra de onde vem cada valor; reset devolve ao .env.
func TestComandoSettings(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	look := envDeTeste(map[string]string{"PATCHD_PANEL_TITLE": "patchd · lab", "PATCHD_OPERATOR": "thyago"})
	s, _ := newSettings(ctx, look, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "Painel", "PATCHD_RETENTION_HISTORY_DAYS": "120"}, "admin"); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := settingsCommand(ctx, st, look, []string{"list"}, &out, &errb); code != exitOK {
		t.Fatalf("list: %d %s", code, errb.String())
	}
	o := out.String()
	for _, tr := range []string{"PATCHD_PANEL_TITLE", "Painel", "tela (admin,", "patchd · lab (.env)", "PATCHD_RETENTION_JOBS_DAYS", "400 dias", "padrão"} {
		if !strings.Contains(o, tr) {
			t.Errorf("list: faltou %q\n%s", tr, o)
		}
	}
	out.Reset()
	if code := settingsCommand(ctx, st, look, []string{"reset", "PATCHD_PANEL_TITLE"}, &out, &errb); code != exitOK ||
		!strings.Contains(out.String(), "restaurado: Nome do painel (PATCHD_PANEL_TITLE): Painel → patchd · lab") {
		t.Errorf("reset: %d %s", code, out.String())
	}
	if code := settingsCommand(ctx, st, look, []string{"reset", "PATCHD_LDAP_HOST"}, &out, &errb); code != exitConfig {
		t.Errorf("fora da lista: %d", code)
	}
	out.Reset()
	if code := settingsCommand(ctx, st, look, []string{"reset", "-all"}, &out, &errb); code != exitOK || !strings.Contains(out.String(), "120 dias → 90 dias") {
		t.Errorf("reset -all: %s", out.String())
	}
	if l, _ := st.Settings(ctx); len(l) != 0 {
		t.Errorf("sobrou: %+v", l)
	}
	if cliAction([]string{"settings", "reset", "PATCHD_PANEL_TITLE"}) != "cli.settings.reset" || cliAction([]string{"settings", "list"}) != "" {
		t.Error("auditoria do terminal")
	}
}

// O catálogo: desligado pela tela, o laço espera; ligado, sincroniza sem reiniciar o servidor.
func TestCatalogoLigadoPelaTela(t *testing.T) {
	banco := novoBanco()
	var iv atomic.Int64 // 0: desligado
	mudou := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go catalogLoopWith(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), banco, fontesDeTeste(t),
		syncOptions{Sources: map[string]bool{"kev": true}, Trigger: "agendada"}, func() time.Duration { return time.Duration(iv.Load()) }, time.Millisecond, mudou)
	time.Sleep(50 * time.Millisecond)
	if n := len(banco.registros()); n != 0 {
		t.Fatalf("desligado rodou %d vez(es)", n)
	}
	iv.Store(int64(time.Hour))
	mudou <- struct{}{}
	fim := time.Now().Add(5 * time.Second)
	for len(banco.registros()) == 0 {
		if time.Now().After(fim) {
			t.Fatal("ligado pela tela e não sincronizou")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(banco.registros()); n != 1 {
		t.Errorf("com 1 h de intervalo, uma vez só: %d", n)
	}
}

// A retenção lê os prazos a cada passada.
func TestRetencaoDaTela(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	s, _ := newSettings(ctx, envDeTeste(nil), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cur := liveRetention(s, store.Retention{})
	if cur().History != 90*24*time.Hour {
		t.Errorf("padrão: %v", cur())
	}
	_, _ = s.Apply(ctx, map[string]string{"PATCHD_RETENTION_HISTORY_DAYS": "45", "PATCHD_RETENTION_AUDIT_DAYS": "0"}, "admin")
	if r := cur(); r.History != 45*24*time.Hour || r.Audit != 0 {
		t.Errorf("da tela: %+v", r)
	}
	catalog, ret := liveSystem(s, true, cur)()
	if catalog != "sincronizado a cada 6h0m0s" || !strings.Contains(ret, "45 dias") || !strings.Contains(ret, "auditoria guardada sempre") {
		t.Errorf("aba Sistema: %q %q", catalog, ret)
	}
}
