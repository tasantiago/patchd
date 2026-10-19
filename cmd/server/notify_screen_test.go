package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

// Aula 19.3: as notificações pela tela. Cada campo conferido; o conjunto tem de montar um
// Mailer (senha só em arquivo); o Mailer é remontado na hora, sem reiniciar.
func TestNotificacoesPelaTela(t *testing.T) {
	ctx := context.Background()
	defs := map[string]settings.Def{}
	for _, d := range settingDefs(envDeTeste(nil)) {
		defs[d.Key] = d
	}
	for _, c := range []struct {
		key, in, out string
		ok           bool
	}{
		{"PATCHD_SMTP_ADDR", " smtp.exemplo:587 ", "smtp.exemplo:587", true},
		{"PATCHD_SMTP_ADDR", "", "", true},
		{"PATCHD_SMTP_ADDR", "smtp.exemplo", "", false},
		{"PATCHD_SMTP_ADDR", "smtp.exemplo:99999", "", false},
		{"PATCHD_SMTP_TLS", "TLS", "tls", true},
		{"PATCHD_SMTP_TLS", "ssl", "", false},
		{"PATCHD_SMTP_USER", "conta servico", "", false},
		{"PATCHD_SMTP_FROM", "patchd <patchd@exemplo>", "patchd <patchd@exemplo>", true},
		{"PATCHD_SMTP_FROM", "patchd@", "", false},
		{"PATCHD_NOTIFY_TO", " a@exemplo ; b@exemplo,", "a@exemplo, b@exemplo", true},
		{"PATCHD_NOTIFY_TO", "a@exemplo, nao-e-email", "", false},
		{"PATCHD_NOTIFY_EVENTS", "fontes,pedidos", "fontes,pedidos", true},
		{"PATCHD_NOTIFY_EVENTS", "", "", false},
		{"PATCHD_NOTIFY_EVENTS", "pedidos,chuva", "", false},
	} {
		got, err := defs[c.key].Check(c.in)
		if (err == nil) != c.ok || got != c.out {
			t.Errorf("%s=%q: %q %v", c.key, c.in, got, err)
		}
	}
	for k := range defs {
		if strings.Contains(k, "PASSWORD") || strings.Contains(k, "CA_FILE") {
			t.Errorf("%s não vai para a tela", k)
		}
	}

	addr, enviadas := relaySemTLS(t)
	senha := filepath.Join(t.TempDir(), "smtp_password")
	env := map[string]string{"PATCHD_SMTP_PASSWORD_FILE": senha}
	st := store.NewMemory()
	s, _ := newSettings(ctx, envDeTeste(env), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	live := newLiveNotifier(s.Lookup)
	if live.Enabled() || live.Describe() != "desligadas" {
		t.Fatalf("partida: %v %q", live.Enabled(), live.Describe())
	}

	// O conjunto: com usuário, a senha tem de estar no arquivo; sem TLS não aceita usuário.
	_, err := s.Apply(ctx, map[string]string{"PATCHD_SMTP_ADDR": addr, "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo",
		"PATCHD_SMTP_USER": "conta"}, "admin")
	var rej *settings.RejectedError
	if err == nil || !strings.Contains(err.Error(), "PATCHD_SMTP_PASSWORD") || !asRejected(err, &rej) {
		t.Fatalf("sem senha: %v", err)
	}
	_ = os.WriteFile(senha, []byte("segredo\n"), 0o600)
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_SMTP_ADDR": addr, "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo",
		"PATCHD_SMTP_USER": "conta", "PATCHD_SMTP_TLS": "none"}, "admin"); err == nil || !strings.Contains(err.Error(), "texto pela rede") {
		t.Fatalf("none com usuário: %v", err)
	}
	if l, _ := st.Settings(ctx); len(l) != 0 {
		t.Fatal("recusado gravou")
	}

	// Ligadas pela tela: relay interno sem TLS, sem usuário.
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_SMTP_ADDR": addr, "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo",
		"PATCHD_SMTP_TLS": "none", "PATCHD_NOTIFY_EVENTS": "pedidos"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if !live.Enabled() || !strings.Contains(live.Describe(), "(none) · de patchd@exemplo · 1 destinatário(s) · avisos: pedidos") {
		t.Fatalf("ligadas: %q", live.Describe())
	}
	if err := live.Send(ctx, "teste", "corpo"); err != nil || enviadas() != 1 {
		t.Fatalf("mandar: %v %d", err, enviadas())
	}
	m1, _, _ := live.current()
	if m2, _, _ := live.current(); m1 != m2 {
		t.Error("a mesma configuração não remonta o Mailer")
	}
	// Mudança que não é das notificações não confere o SMTP (um .env quebrado não trava o resto).
	s.CheckAll = func(look settings.Lookup, changed []string) error { return notifyCheck(look, changed) }
	_ = os.Remove(senha)
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "Painel"}, "admin"); err != nil {
		t.Errorf("título com o SMTP quebrado: %v", err)
	}
	// Desligar: apagar o servidor.
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_SMTP_ADDR": ""}, "admin"); err != nil {
		t.Fatal(err)
	}
	if live.Enabled() || live.Send(ctx, "x", "y") == nil {
		t.Error("desligadas pela tela")
	}
}

func asRejected(err error, rej **settings.RejectedError) bool {
	r, ok := err.(*settings.RejectedError)
	*rej = r
	return ok
}
