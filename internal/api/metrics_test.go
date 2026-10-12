package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/metrics"
)

// TestMetricasDaAPI (Aula 10.2): a rota vem do padrão do ServeMux, nunca do caminho cru;
// check-ins por canal e logins por resultado.
func TestMetricasDaAPI(t *testing.T) {
	reg := metrics.NewRegistry()
	h, st, _ := ambientePaginas(t, api.WithMetrics(api.NewMetrics(reg)))
	ctx := context.Background()

	pagina(h, "GET", "/painel/entrar", "", nil)
	pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoAdmin, nil)
	pagina(h, "GET", "/nao/existe", "", nil)
	pagina(h, "POST", "/painel/entrar", "", url.Values{"usuario": {"ninguem"}, "senha": {"errada"}}, "Origin", "http://example.com")

	// Um check-in de verdade, por HTTP (sem TLS no teste).
	_, th := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, th, "t", time.Now().Add(time.Hour), 1)
	cred, ch := identity.NewSecret(identity.CredentialPrefix)
	id := identity.NewMachineID()
	if _, err := st.Enroll(ctx, th, identity.NewMachine{ID: id, CredentialHash: ch}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/agent/checkin", strings.NewReader(`{"agent_version":"v1","sent_at":"`+time.Now().UTC().Format(time.RFC3339)+`"}`))
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("check-in: %d %s", rec.Code, rec.Body.String())
	}

	var b strings.Builder
	_ = reg.WriteText(&b)
	out := b.String()
	for _, quer := range []string{
		`patchd_http_requests_total{rota="GET /painel/entrar",codigo="200"} 1`,
		`patchd_http_requests_total{rota="GET /painel/maquinas/{id}",codigo="200"} 1`,
		`patchd_http_requests_total{rota="(nenhuma)",codigo="404"} 1`,
		`patchd_http_requests_total{rota="POST /api/v1/agent/checkin",codigo="200"} 1`,
		`patchd_checkins_total{canal="http"} 1`,
		`patchd_logins_total{metodo="senha",resultado="recusado"} 1`,
		`patchd_http_request_seconds_total{rota="GET /painel/entrar"}`,
	} {
		if !strings.Contains(out, quer) {
			t.Errorf("falta %s em:\n%s", quer, out)
		}
	}
	if strings.Contains(out, "60ef97e4") || strings.Contains(out, id) {
		t.Error("ID de máquina virou rótulo")
	}
}
