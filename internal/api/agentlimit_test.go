package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
)

// TestLimitePorMaquinaNaAPI (Aula 20.5): o 11º inventário seguido da mesma máquina leva 429
// com Retry-After; a outra máquina e o check-in seguem.
func TestLimitePorMaquinaNaAPI(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 2)
	m1 := registra(t, h, tok)
	m2 := registra(t, h, tok) // mesma evidência: outra máquina (o clone vira alerta)
	for i := range 10 {
		if r := envia(h, "POST", "/api/v1/agent/inventory", m1.Credential, inventario(t, "26.0"+string(rune('0'+i)))); r.Code == http.StatusTooManyRequests {
			t.Fatalf("inventário %d recusado dentro da rajada", i+1)
		}
	}
	r := envia(h, "POST", "/api/v1/agent/inventory", m1.Credential, inventario(t, "26.99"))
	if r.Code != http.StatusTooManyRequests || r.Header().Get("Retry-After") != "180" {
		t.Fatalf("11º inventário: %d Retry-After=%q %s", r.Code, r.Header().Get("Retry-After"), r.Body)
	}
	if r := envia(h, "POST", "/api/v1/agent/inventory", m2.Credential, inventario(t, "26.01")); r.Code == http.StatusTooManyRequests {
		t.Error("a outra máquina pagou pela primeira")
	}
	if r := envia(h, "POST", "/api/v1/agent/checkin", m1.Credential, []byte(`{"inventory_hash":""}`)); r.Code == http.StatusTooManyRequests {
		t.Error("o check-in pagou pelos inventários")
	}
}
