package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Aula 20.2: os achados do gosec que viraram correção ganham teste.

func TestUsedPct(t *testing.T) {
	for _, c := range []struct {
		total, free uint64
		want        int
	}{
		{100, 10, 90},
		{100, 100, 0},
		{0, 0, 0},
		{10, 20, 0},                      // livre maior que o total: o agente mentiu
		{1 << 63, 0, 100},                // (total-livre)*100 estouraria o uint64
		{^uint64(0), ^uint64(0) / 2, 50}, // idem
		{1000, 999, 0},                   // 0,1 % arredonda para baixo, como antes
	} {
		if got := usedPct(c.total, c.free); got != c.want {
			t.Errorf("usedPct(%d, %d) = %d, esperado %d", c.total, c.free, got, c.want)
		}
	}
}

func TestRedirectPatch(t *testing.T) {
	for in, want := range map[string]string{
		"/painel/patch?status=aguardando": "/painel/patch?status=aguardando",
		"/painel/patchwork":               "/painel/patchwork", // mesmo prefixo, ainda local
		"https://exemplo.invalid/x":       "/painel/patch",
		"//exemplo.invalid/painel/patch":  "/painel/patch",
		"/painel/":                        "/painel/patch",
	} {
		w := httptest.NewRecorder()
		redirectPatch(w, httptest.NewRequest(http.MethodPost, "/painel/patch/aprovar-lote", nil), in)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
			t.Errorf("%q: %d %q, esperado %q", in, w.Code, w.Header().Get("Location"), want)
		}
	}
}
