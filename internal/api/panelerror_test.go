package api_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// bancoQueFalha: a lista de máquinas falha (o banco fora, uma consulta estourando o prazo).
type bancoQueFalha struct{ *store.Memory }

func (bancoQueFalha) Machines(context.Context) ([]protocol.MachineSummary, error) {
	return nil, errors.New("banco fora")
}

// Uma falha interna numa tela do painel vira uma página, com o código do pedido que acha a
// linha no log, e não o JSON da API; a API continua em JSON.
func TestFalhaInternaNoPainel(t *testing.T) {
	agora := time.Now().UTC()
	st := store.NewMemory()
	if err := st.CreatePanelSession(context.Background(), identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	h := api.New(bancoQueFalha{st}, slog.New(slog.NewTextHandler(log, nil)))
	r := pagina(h, "GET", "/painel/grupos-implantacao", sessaoAdmin, nil)
	id := r.Header().Get("X-Request-ID")
	b := r.Body.String()
	if r.Code != http.StatusInternalServerError || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(b, "Algo deu errado") || id == "" || !strings.Contains(b, "<code>"+id+"</code>") || strings.Contains(b, "banco fora") {
		t.Fatalf("página de erro: %d %s\n%s", r.Code, r.Header().Get("Content-Type"), b)
	}
	if !strings.Contains(log.String(), "banco fora") || !strings.Contains(log.String(), "request_id="+id) {
		t.Errorf("log:\n%s", log)
	}
}
