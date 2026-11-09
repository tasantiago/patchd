package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
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

// O relé (Parâmetros › Agentes) vai no check-in; vazio, vai vazio (o agente esquece o que
// tinha); sem o serviço de parâmetros, nem vai (o agente mantém).
func TestReleNoCheckin(t *testing.T) {
	for valor, quer := range map[string]string{"203.0.113.10:8443": "203.0.113.10:8443", "": "", "lixo": ""} {
		h, st, _ := ambientePaginas(t, api.WithSettings(parametrosFixos{api.RelayKey: valor}))
		tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
		st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 1)
		m := registra(t, h, tok)
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo).Body.Bytes(), &resp)
		if resp.Relay == nil || resp.Relay.Addr != quer {
			t.Errorf("%q: %+v", valor, resp.Relay)
		}
	}
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 1)
	m := registra(t, h, tok)
	corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
	if r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo); strings.Contains(r.Body.String(), `"relay"`) {
		t.Error("sem parâmetros, o relé não vai")
	}
}

// Token pelo relé: criado no painel (uso único, até 7 dias, só com o relé configurado), com o
// -relay no comando; vale pelo relé uma vez só, e não pela rede interna.
func TestTokenPeloRele(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	f := url.Values{"nota": {"notebook"}, "validade": {"24"}, "usos": {"5"}, "rele": {"1"}}
	if r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, f); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "configure antes o endereço do relé") {
		t.Errorf("sem relé configurado: %d", r.Code)
	}

	h, st, _ := ambientePaginas(t, api.WithSettings(parametrosFixos{api.RelayKey: "203.0.113.10:8443"}))
	if r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, url.Values{"validade": {"720"}, "usos": {"1"}, "rele": {"1"}}); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "de 1 hora a 7 dias") {
		t.Errorf("validade longa: %d", r.Code)
	}
	r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, f)
	b := r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "-relay 203.0.113.10:8443") || !strings.Contains(b, "Token <b>pelo relé</b>") {
		t.Fatalf("token criado: %d\n%s", r.Code, b)
	}
	tok := regexp.MustCompile(`patchd_enr_[A-Za-z0-9_-]+`).FindString(b)
	l, _ := st.EnrollmentTokens(context.Background())
	if len(l) != 1 || !l[0].Relay || l[0].MaxUses != 1 {
		t.Fatalf("token: %+v", l)
	}
	if b := pagina(h, "GET", "/painel/configuracao?aba=agente", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "pelo relé</span>") {
		t.Error("lista sem a marca")
	}
	corpo, _ := json.Marshal(protocol.EnrollRequest{AgentVersion: "v", Evidence: protocol.IdentityEvidence{InstallID: "guid-rele", Hostname: "nb", OSFamily: "windows"}})
	if r := envia(h, "POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusUnauthorized {
		t.Errorf("token pelo relé na rede interna: %d", r.Code)
	}
	viaRele := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r.WithContext(api.ViaRelay(r.Context()))) })
	if r := envia(viaRele, "POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusCreated {
		t.Fatalf("pelo relé: %d %s", r.Code, r.Body)
	}
	if r := envia(viaRele, "POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusUnauthorized {
		t.Errorf("segundo uso: %d", r.Code)
	}
}
