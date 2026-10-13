package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/store"
)

// Na conferência da campanha de update, a fonte sem nenhuma correção aprovada é avisada.
func TestCampanhaAvisaFonteSemAprovacao(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	_, _ = st.ApprovePatch(t.Context(), store.PatchApproval{Item: "curl", FixedIn: "8.18.0-1ubuntu2.10", ApprovedBy: "ana"})
	f := url.Values{"nome": {"update de outubro"}, "tipo": {"update"}, "fontes": {"curl, rsync"}, "anel": {"0"},
		"observacao": {"24"}, "quando": {"prazo"}, "prazo": {"24"}}
	b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	if !strings.Contains(b, "A fonte rsync não tem nenhuma correção aprovada em Patch") || strings.Contains(b, "A fonte curl não tem") ||
		!strings.Contains(b, "com a correção aprovada em Patch") {
		t.Errorf("conferência:\n%s", b)
	}
}

func TestFavicon(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	// Público: a tela de login também tem o ícone.
	r := pagina(h, "GET", "/painel/favicon.svg", "", nil)
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(r.Body.String(), "<svg") ||
		!strings.Contains(r.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("favicon: %d %v", r.Code, r.Header())
	}
	for p, cookie := range map[string]string{"/painel/entrar": "", "/painel/inventario": sessaoTeste} {
		r := pagina(h, "GET", p, cookie, nil)
		if !strings.Contains(r.Body.String(), `<link rel="icon" href="/painel/favicon.svg" type="image/svg+xml">`) ||
			!strings.Contains(r.Header().Get("Content-Security-Policy"), "img-src 'self'") {
			t.Errorf("%s: sem o ícone ou sem img-src", p)
		}
	}
}
