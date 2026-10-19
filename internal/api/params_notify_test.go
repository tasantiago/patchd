package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

// carteiroComChave: um Notifier que liga e desliga (como o do servidor, que segue a tela).
type carteiroComChave struct {
	carteiro
	ligado atomic.Bool
}

func (c *carteiroComChave) Enabled() bool { return c.ligado.Load() }

// Aula 19.3: o cartão Notificações com as caixas dos avisos e o botão de teste que volta para
// a aba; ligar e desligar sem reiniciar.
func TestNotificacoesNaAbaParametros(t *testing.T) {
	st0 := store.NewMemory()
	defs := []settings.Def{
		{Key: "PATCHD_SMTP_ADDR", Group: "Notificações", Label: "Servidor SMTP", Check: func(v string) (string, error) { return strings.TrimSpace(v), nil }},
		{Key: "PATCHD_NOTIFY_EVENTS", Group: "Notificações", Label: "Avisos", Kind: settings.Multi, Default: "pedidos,campanhas,fontes",
			Options: []settings.Option{{Value: "pedidos", Label: "pedidos esperando a TI"}, {Value: "campanhas", Label: "campanhas pausadas"}, {Value: "fontes", Label: "fontes de busca falhando"}},
			Check: func(v string) (string, error) {
				if v == "" {
					return "", errors.New("marque ao menos um aviso")
				}
				return v, nil
			}},
	}
	params := settings.New(defs, func(string) (string, bool) { return "", false }, st0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = params.Refresh(context.Background())
	c := &carteiroComChave{}
	h, _, _ := ambientePaginas(t, api.WithSettings(params), api.WithNotifier(c))

	b := pagina(h, "GET", "/painel/configuracao?aba=parametros", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<input type="hidden" name="PATCHD_NOTIFY_EVENTS" value="">`) ||
		!strings.Contains(b, `<input type="checkbox" name="PATCHD_NOTIFY_EVENTS" value="fontes" checked> fontes de busca falhando`) ||
		strings.Contains(b, "Mandar e-mail de teste") {
		t.Errorf("cartão desligado:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "desligadas (sem PATCHD_SMTP_ADDR)") {
		t.Error("aba Sistema: desligadas")
	}

	// Duas caixas marcadas (o campo escondido vazio também vem).
	r := pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Notificações"}, "PATCHD_SMTP_ADDR": {"smtp.exemplo:587"},
		"PATCHD_NOTIFY_EVENTS": {"", "pedidos", "fontes"}})
	if r.Code != 303 || params.Value("PATCHD_NOTIFY_EVENTS") != "pedidos,fontes" {
		t.Fatalf("caixas: %d %q", r.Code, params.Value("PATCHD_NOTIFY_EVENTS"))
	}
	// Nenhuma marcada: recusado (para desligar, apaga o servidor).
	r = pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Notificações"}, "PATCHD_NOTIFY_EVENTS": {""}})
	if r.Code != 422 || !strings.Contains(r.Body.String(), "marque ao menos um aviso") {
		t.Errorf("nenhuma: %d", r.Code)
	}

	// Ligadas (o servidor remontou o Mailer): o botão aparece e volta para a aba.
	c.ligado.Store(true)
	b = pagina(h, "GET", "/painel/configuracao?aba=parametros", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `formaction="/painel/configuracao/notificacoes/teste" name="volta" value="parametros">Mandar e-mail de teste</button>`) {
		t.Error("sem o botão de teste")
	}
	r = pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, url.Values{"volta": {"parametros"}})
	if r.Header().Get("Location") != "/painel/configuracao?aba=parametros&notificacao=enviada" || len(c.msgs) != 1 {
		t.Fatalf("teste: %s %d", r.Header().Get("Location"), len(c.msgs))
	}
	if b := pagina(h, "GET", r.Header().Get("Location"), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Mensagem de teste aceita pelo SMTP") {
		t.Error("aviso do teste na aba Parâmetros")
	}
	// Desligadas de novo: o teste recusa sem mandar.
	c.ligado.Store(false)
	c.carteiro.mu.Lock()
	n := len(c.msgs)
	c.carteiro.mu.Unlock()
	r = pagina(h, "POST", "/painel/configuracao/notificacoes/teste", sessaoAdmin, url.Values{"volta": {"parametros"}})
	if r.Header().Get("Location") != "/painel/configuracao?aba=parametros&erro=notificacao-desligada" || len(c.msgs) != n {
		t.Errorf("desligadas: %s", r.Header().Get("Location"))
	}
}
