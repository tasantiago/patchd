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
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Aula 17.6: o usuário escolhe a hora do reinício.
func TestReinicioPeloAutoatendimento(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(t.TempDir(), nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	maquina := func(name, edition string) protocol.EnrollResponse {
		tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
		_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
		maq := registra(t, h, tok)
		_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
			OS: protocol.OSInfo{ID: "windows", Name: name, Edition: edition, Arch: "amd64"}})
		sim := true
		_ = st.SaveScan(ctx, maq.MachineID, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
			Reboot: &protocol.RebootStatus{Pending: &sim}})
		return maq
	}
	pc := maquina("Windows 11 Pro", "Professional")
	ana := protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}}
	loja := func(maq protocol.EnrollResponse) protocol.SelfServiceCatalog {
		t.Helper()
		body, _ := json.Marshal(ana)
		r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
		var c protocol.SelfServiceCatalog
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &c) != nil {
			t.Fatalf("loja: %d %s", r.Code, r.Body)
		}
		return c
	}
	pede := func(maq protocol.EnrollResponse, at *time.Time) protocol.SelfServiceResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: ana, Action: protocol.SelfServiceReboot, RebootAt: at})
		r := envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body)
		var resp protocol.SelfServiceResponse
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &resp) != nil {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		return resp
	}

	// A loja diz que há reinício pendente e que o usuário pode escolher a hora (sem grupo:
	// o reinício é da máquina dele, não um programa da oferta).
	c := loja(pc)
	if c.Reboot == nil || c.Reboot.Pending == nil || !*c.Reboot.Pending || !c.Reboot.Allowed || c.Reboot.JobID != 0 {
		t.Fatalf("reinício na loja: %+v", c.Reboot)
	}
	longe := time.Now().Add(13 * time.Hour)
	if r := pede(pc, &longe); r.Status != protocol.SelfServiceRefused || !strings.Contains(r.Message, "12 horas") {
		t.Errorf("13 h à frente: %+v", r)
	}
	as18 := time.Now().Add(4 * time.Hour).UTC().Truncate(time.Second)
	r := pede(pc, &as18)
	if r.Status != protocol.SelfServiceCreated || r.JobID == 0 {
		t.Fatalf("às 18h: %+v", r)
	}
	j, _, _ := st.JobForMachine(ctx, pc.MachineID, r.JobID)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	p, err := jobs.ParseRebootParams(payload.Params)
	if err != nil || j.Type != jobs.TypeReboot || p.At == nil || !p.At.Equal(as18) || j.CreatedBy != `autoatendimento: EXEMPLO\ana` ||
		j.NotAfter.Before(as18.Add(30*time.Minute)) {
		t.Errorf("job: %+v %+v %v", j, p, err)
	}
	// A loja mostra o reinício pedido, com a hora; Meus pedidos também.
	c = loja(pc)
	if c.Reboot.JobID != r.JobID || c.Reboot.At == nil || !c.Reboot.At.Equal(as18) || len(c.Requests) != 1 || c.Requests[0].Name != "Reinício do computador" {
		t.Errorf("depois do pedido: %+v %+v", c.Reboot, c.Requests)
	}
	if r2 := pede(pc, nil); r2.Status != protocol.SelfServiceWait || r2.JobID != r.JobID {
		t.Errorf("segundo pedido: %+v", r2)
	}

	// Servidor: o reinício derruba a sessão de todos; fica com a TI.
	srv := maquina("Windows Server 2022 Standard", "ServerStandard")
	if c := loja(srv); c.Reboot == nil || c.Reboot.Allowed {
		t.Errorf("servidor na loja: %+v", c.Reboot)
	}
	if r := pede(srv, nil); r.Status != protocol.SelfServiceRefused || !strings.Contains(r.Message, "equipe de TI") {
		t.Errorf("servidor: %+v", r)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."})
	var pedido string
	for _, e := range ev {
		if e.Action == "autoatendimento.pedido" {
			pedido = e.Detail
		}
	}
	if !strings.Contains(pedido, "· computador "+as18.Format(time.RFC3339)+" · reiniciar") {
		t.Errorf("auditoria: %q", pedido)
	}
}
