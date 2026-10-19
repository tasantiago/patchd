package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/store"
)

// carteiro: um Notifier de mentira que guarda o que mandou.
type carteiro struct {
	mu    sync.Mutex
	falha error
	msgs  []string
}

func (c *carteiro) Describe() string {
	return "smtp.exemplo:587 (starttls) · de patchd@exemplo · 2 destinatário(s)"
}
func (c *carteiro) Send(_ context.Context, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.falha != nil {
		return c.falha
	}
	c.msgs = append(c.msgs, subject+"\n"+body)
	return nil
}

// Aula 18.5: a linha na aba Sistema, o botão de teste (só admin), o intervalo e a auditoria.
func TestNotificacoesNoPainel(t *testing.T) {
	ctx := context.Background()
	h, _, _ := ambientePaginas(t)
	b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<dt>Notificações</dt><dd class="nivel-atencao">desligadas (sem PATCHD_SMTP_ADDR)`) || strings.Contains(b, "notificacoes/teste") {
		t.Error("desligadas: a linha avisa e o botão não aparece")
	}
	if r := pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, nil); r.Code != http.StatusSeeOther || !strings.Contains(r.Header().Get("Location"), "notificacao-desligada") {
		t.Errorf("teste desligado: %d %s", r.Code, r.Header().Get("Location"))
	}

	c := &carteiro{}
	h, st, _ := ambientePaginas(t, api.WithNotifier(c), api.WithPublicURL("https://patchd.exemplo:8443"))
	b = pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<dt>Notificações</dt><dd>ligadas · smtp.exemplo:587 (starttls)") || !strings.Contains(b, `action="/painel/configuracao/notificacoes/teste"`) {
		t.Error("ligadas: a linha e o botão")
	}
	if r := pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura manda teste: %d", r.Code)
	}
	r := pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, nil)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/configuracao?notificacao=enviada" {
		t.Fatalf("teste: %d %s", r.Code, r.Header().Get("Location"))
	}
	if len(c.msgs) != 1 || !strings.Contains(c.msgs[0], "Teste de notificação\nMensagem de teste pedida por thyago") ||
		!strings.Contains(c.msgs[0], "Painel: https://patchd.exemplo:8443/painel/configuracao") {
		t.Errorf("mensagem: %q", c.msgs)
	}
	if b := pagina(h, "GET", "/painel/configuracao?notificacao=enviada", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Mensagem de teste aceita pelo SMTP") {
		t.Error("aviso de enviado")
	}
	// Logo em seguida: espera.
	if r := pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, nil); !strings.Contains(r.Header().Get("Location"), "notificacao-espera") || len(c.msgs) != 1 {
		t.Errorf("segundo teste em seguida: %s, %d mensagens", r.Header().Get("Location"), len(c.msgs))
	}
	l, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "notificacao.teste"})
	if len(l) != 1 || l[0].Username != "thyago" || l[0].Detail != "aceita pelo SMTP" {
		t.Errorf("auditoria: %+v", l)
	}

	// Falha: a página mostra o erro, a trilha e o log guardam.
	c2 := &carteiro{falha: errors.New("SMTP: autenticação recusada: 535 5.7.8 credenciais inválidas")}
	h, st, log := ambientePaginas(t, api.WithNotifier(c2))
	r = pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, nil)
	if r.Code != http.StatusBadGateway || !strings.Contains(r.Body.String(), "O teste falhou: SMTP: autenticação recusada: 535 5.7.8") {
		t.Errorf("falha: %d", r.Code)
	}
	if l, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "notificacao.teste"}); len(l) != 1 || !strings.HasPrefix(l[0].Detail, "falhou: SMTP: autenticação recusada") {
		t.Errorf("auditoria da falha: %+v", l)
	}
	if !strings.Contains(log.String(), "teste de notificação falhou") {
		t.Error("log da falha")
	}
}
