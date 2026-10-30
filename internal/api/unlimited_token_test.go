package api_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestPanelUnlimitedEnrollmentToken(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, url.Values{"nota": {"ISO TJ"}, "validade": {"0"}, "usos": {"0"}})
	if r.Code != 200 || !strings.Contains(r.Body.String(), "sem vencimento") || !strings.Contains(r.Body.String(), "usos ilimitados") {
		t.Fatalf("unlimited token form: %d", r.Code)
	}
	tokens, err := st.EnrollmentTokens(context.Background())
	if err != nil || len(tokens) != 1 || tokens[0].MaxUses != 0 || !tokens[0].ExpiresAt.IsZero() {
		t.Fatal("incorrect unlimited token", err)
	}
}
