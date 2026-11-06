package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/canal"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O endpoint do canal: ligado, o aviso guardado volta na hora; desligado em Parâmetros, a
// resposta diz para o agente parar, e o check-in também. Sem credencial, 401.
func TestEndpointDoCanal(t *testing.T) {
	for _, ligado := range []bool{true, false} {
		v := "1"
		if !ligado {
			v = "0"
		}
		hub := canal.New()
		h, st, _ := ambientePaginas(t, api.WithChannel(hub), api.WithSettings(parametrosFixos{api.ChannelKey: v}))
		tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
		st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 1)
		m := registra(t, h, tok)
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
		var ci protocol.CheckinResponse
		_ = json.Unmarshal(envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo).Body.Bytes(), &ci)
		if ci.Channel != ligado {
			t.Errorf("ligado %v: check-in diz %v", ligado, ci.Channel)
		}
		hub.Notify(m.MachineID, "job")
		start := time.Now()
		r := envia(h, "POST", protocol.ChannelPath, m.Credential, []byte("{}"))
		var resp protocol.ChannelResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		if r.Code != 200 || resp.Enabled != ligado || resp.Wake != ligado || time.Since(start) > 2*time.Second {
			t.Errorf("ligado %v: %d %+v em %v", ligado, r.Code, resp, time.Since(start))
		}
		if r := envia(h, "POST", protocol.ChannelPath, "", []byte("{}")); r.Code != 401 {
			t.Errorf("sem credencial: %d", r.Code)
		}
	}
}
