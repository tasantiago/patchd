package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// docs/rele.md: o check-in guarda por onde a máquina falou (pela porta só de agentes, o relé,
// ou pela de dentro), e o Resumo da máquina mostra.
func TestCheckinPeloRele(t *testing.T) {
	ctx := context.Background()
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 1)
	m := registra(t, h, tok)
	rele := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r.WithContext(api.ViaRelay(r.Context()))) })
	corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
	if v, _ := st.CheckinRelay(ctx, m.MachineID); v != nil {
		t.Fatalf("antes do check-in: %v", *v)
	}
	envia(rele, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
	if v, _ := st.CheckinRelay(ctx, m.MachineID); v == nil || !*v {
		t.Errorf("pelo relé: %v", v)
	}
	envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
	if v, _ := st.CheckinRelay(ctx, m.MachineID); v == nil || *v {
		t.Errorf("de dentro: %v", v)
	}

	const conhecida = "60ef97e4-0001" // a máquina que o compliance de teste conhece
	if b := pagina(h, "GET", "/painel/maquinas/"+conhecida, sessaoAdmin, nil).Body.String(); strings.Contains(b, "<dt>Rede</dt>") {
		t.Error("sem check-in desde a 0064, sem a linha")
	}
	st.SetCheckinRelay(ctx, conhecida, true)
	if b := pagina(h, "GET", "/painel/maquinas/"+conhecida, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dt>Rede</dt><dd>fora da rede (pelo relé na nuvem)</dd>") {
		t.Errorf("máquina fora da rede:\n%s", b)
	}
}
