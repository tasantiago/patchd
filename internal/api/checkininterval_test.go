package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
)

type parametrosFixos map[string]string

func (p parametrosFixos) Value(k string) string  { return p[k] }
func (parametrosFixos) Groups() []settings.Group { return nil }
func (parametrosFixos) Apply(context.Context, map[string]string, string) ([]settings.Change, error) {
	return nil, nil
}
func (parametrosFixos) Reset(context.Context, string, string) (settings.Change, bool, error) {
	return settings.Change{}, false, nil
}

// O intervalo de check-in de Configuração › Parâmetros › Agentes vai em cada check-in; "0"
// (o padrão) e valores fora dos limites não vão (vale o de cada agente).
func TestIntervaloDeCheckinNoCheckin(t *testing.T) {
	for valor, quer := range map[string]int{"15": 15, "0": 0, "": 0, "2": 0, "99999": 0} {
		h, st, _ := ambientePaginas(t, api.WithSettings(parametrosFixos{"PATCHD_CHECKIN_MINUTES": valor}))
		tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
		if _, err := st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 1); err != nil {
			t.Fatal(err)
		}
		m := registra(t, h, tok)
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
		r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		if resp.CheckinMinutes != quer {
			t.Errorf("%q: mandou %d, quero %d", valor, resp.CheckinMinutes, quer)
		}
		// O aviso do painel segue o intervalo.
		espera := "até cerca de 1 h"
		if quer == 15 {
			espera = "até cerca de 15 min"
		}
		if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?criado=1&job=3", sessaoAdmin, nil).Body.String(); !strings.Contains(b, espera) {
			t.Errorf("%q: o aviso não diz %q", valor, espera)
		}
	}
}
