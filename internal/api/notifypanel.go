package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
)

// Notificações por e-mail (Aula 18.5) no painel: a linha na aba Sistema e o botão de teste.
// A configuração continua só no ambiente; o painel mostra o resumo, sem a senha.

// Notifier manda e-mail para a lista da equipe (internal/notify).
type Notifier interface {
	Describe() string
	Send(ctx context.Context, subject, body string) error
}

// notifyTestGap: um teste por vez e, no máximo, um a cada 30 s (o botão não vira spam).
const notifyTestGap = 30 * time.Second

type notifierState struct {
	n    Notifier
	mu   sync.Mutex
	last time.Time
}

// WithNotifier liga as notificações por e-mail.
func WithNotifier(n Notifier) Option {
	return func(a *api) {
		if n != nil {
			a.notifier = &notifierState{n: n}
		}
	}
}

// notifyRow: a linha da aba Sistema.
func (a *api) notifyRow() (value, level string) {
	if a.notifier == nil {
		return "desligadas (sem PATCHD_SMTP_ADDR): pedidos esperando a TI e campanhas pausadas só aparecem no painel", "atencao"
	}
	return "ligadas · " + a.notifier.n.Describe(), ""
}

// notifyTestAction: POST /painel/configuracao/notificacoes/teste, só admin.
func (a *api) notifyTestAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.notifier == nil {
		http.Redirect(w, r, "/painel/configuracao?erro=notificacao-desligada", http.StatusSeeOther)
		return
	}
	ns := a.notifier
	ns.mu.Lock()
	if wait := notifyTestGap - a.now().Sub(ns.last); !ns.last.IsZero() && wait > 0 {
		ns.mu.Unlock()
		http.Redirect(w, r, "/painel/configuracao?erro=notificacao-espera", http.StatusSeeOther)
		return
	}
	ns.last = a.now()
	ns.mu.Unlock()

	body := fmt.Sprintf("Mensagem de teste pedida por %s (%s) em %s.\n\nSe chegou, as notificações do patchd chegam a esta lista.",
		s.Username, s.Source, a.now().UTC().Format("02/01/2006 15:04")+" UTC")
	if a.publicURL != "" {
		body += "\n\nPainel: " + strings.TrimRight(a.publicURL, "/") + "/painel/configuracao"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	defer cancel()
	err := ns.n.Send(ctx, "Teste de notificação", body)
	result := "aceita pelo SMTP"
	if err != nil {
		result = "falhou: " + err.Error()
		a.logger.Warn("teste de notificação falhou", "error", err, "username", s.Username, "request_id", RequestID(r.Context()))
	} else {
		a.logger.Info("teste de notificação enviado", "username", s.Username, "request_id", RequestID(r.Context()))
	}
	a.audit(r, s, "notificacao.teste", "e-mail", result)
	if err == nil {
		http.Redirect(w, r, "/painel/configuracao?notificacao=enviada", http.StatusSeeOther)
		return
	}
	v := settingsView{pageBase: a.baseFor(r.Context(), s, "Configuração", "configuracao"), Tab: "sistema", NotifyOn: true}
	v.Error = "O teste falhou: " + shortError(err.Error(), 300) + ". O detalhe também está no log do servidor."
	if v.System, err = a.systemRows(r); err == nil {
		v.Appearance, err = a.logoCard(r.Context())
	}
	if err != nil {
		a.internalError(w, r, "montar a configuração", err)
		return
	}
	v.Sub = v.Tab
	a.render(w, r, http.StatusBadGateway, "configuracao", v)
}

// notifyFlash: as mensagens da aba Sistema.
func notifyFlash(q url.Values, v *pageBase) {
	switch {
	case q.Get("notificacao") == "enviada":
		v.Flash = "Mensagem de teste aceita pelo SMTP. Confira a caixa da lista (e a pasta de spam); se não chegar, o problema está depois do SMTP (regra de encaminhamento, filtro, lista)."
	case q.Get("erro") == "notificacao-espera":
		v.Error = "Espere 30 segundos entre um teste e outro."
	case q.Get("erro") == "notificacao-desligada":
		v.Error = "As notificações estão desligadas: defina PATCHD_SMTP_ADDR e o resto no .env e suba o servidor de novo."
	}
}

// shortError corta a mensagem em n letras, numa linha.
func shortError(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
