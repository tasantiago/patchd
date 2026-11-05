package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Reinício em horário marcado: o job espera o horário (só sai no check-in a partir dele) e vale
// pelo intervalo escolhido; com "ligar pela rede antes", o pacote vai para o repetidor da
// sub-rede, também agendado, 10 minutos antes. Sem repetidor, o reinício fica e o aviso diz.
func TestReinicioEmHorarioMarcado(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	agora := time.Now().UTC()
	if err := st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", agora.Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := jobs.GenerateKey()
	src := &complianceFalso{}
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithJobKey(priv), api.WithTimezone("America/Porto_Velho"), api.WithCompliance(src))
	alvo, rep, longe := registra(t, h, tok), registra(t, h, tok), registra(t, h, tok)
	src.fleet.Machines = []protocol.ComplianceStatus{{MachineID: alvo.MachineID, State: "reboot_pendente", LastSeenAt: agora, EvaluatedAt: agora},
		{MachineID: longe.MachineID, State: "reboot_pendente", LastSeenAt: agora, EvaluatedAt: agora}}
	checkin := func(cred string, s *protocol.MachineState) protocol.CheckinResponse {
		t.Helper()
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), State: s, Capabilities: []string{protocol.CapabilityJobs}})
		r := envia(h, "POST", "/api/v1/agent/checkin", cred, corpo)
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}
	rede := func(mac, ip string) *protocol.MachineState {
		return &protocol.MachineState{CollectedAt: agora, WakeRelay: true, Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: mac, IPv4: []string{ip}, Up: true}}}
	}
	checkin(alvo.Credential, rede("00:11:22:33:44:55", "192.0.2.20/24"))
	checkin(rep.Credential, rede("00:aa:00:00:00:01", "192.0.2.30/24"))
	checkin(longe.Credential, rede("00:aa:00:00:00:02", "198.51.100.4/24"))
	sim := true
	for _, m := range []string{alvo.MachineID, longe.MachineID} {
		_, _ = st.SaveInventory(ctx, m, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + m, OS: protocol.OSInfo{Family: "windows", Hostname: "pc"}})
		_ = st.SaveScan(ctx, m, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, Reboot: &protocol.RebootStatus{Pending: &sim}})
	}

	pvh, _ := time.LoadLocation("America/Porto_Velho")
	em := time.Now().In(pvh).Add(26 * time.Hour).Truncate(time.Minute)
	maq := "/painel/maquinas/" + alvo.MachineID
	b := pagina(h, "GET", maq+"?aba=pendencias", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `value="horario"`) || !strings.Contains(b, `type="datetime-local" name="inicio" value="`) || !strings.Contains(b, "T02:00") ||
		!strings.Contains(b, "fuso America/Porto_Velho") {
		t.Fatalf("formulário:\n%s", b)
	}
	pede := func(m string, f url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", "/painel/maquinas/"+m+"/reiniciar", sessaoAdmin, f))
	}
	form := func(inicio time.Time, validade string, ligar bool) url.Values {
		f := url.Values{"aviso": {"15"}, "quando": {"horario"}, "inicio": {inicio.Format("2006-01-02T15:04")}, "validade": {validade}}
		if ligar {
			f.Set("ligar", "1")
		}
		return f
	}
	// Horário no passado, além de 14 dias ou validade fora da lista: nada é pedido.
	for _, f := range []url.Values{form(time.Now().In(pvh).Add(-time.Hour), "180", false), form(em.Add(15*24*time.Hour), "180", false), form(em, "45", false)} {
		if l := pede(alvo.MachineID, f); !strings.Contains(l, "erro=reinicio-horario") {
			t.Errorf("inválido: %s", l)
		}
	}

	l := pede(alvo.MachineID, form(em, "180", true))
	if !strings.Contains(l, "reinicio=agendado") || !strings.Contains(l, "wol=ok") || !strings.Contains(l, "wn=1") {
		t.Fatalf("agendar: %s", l)
	}
	q, _ := url.ParseQuery(l[strings.Index(l, "?")+1:])
	row, _, _ := st.JobForMachine(ctx, alvo.MachineID, mustID(t, q.Get("job")))
	if row.Type != jobs.TypeReboot || row.NotBefore == nil || !row.NotBefore.Equal(em) || !row.NotAfter.Equal(em.Add(3*time.Hour)) {
		t.Fatalf("job: tipo %s, de %v até %v (queria %v)", row.Type, row.NotBefore, row.NotAfter, em)
	}
	// Nem o reinício nem o pacote saem agora.
	if resp := checkin(alvo.Credential, nil); len(resp.Jobs) != 0 {
		t.Error("o reinício saiu antes da hora")
	}
	if resp := checkin(rep.Credential, nil); len(resp.Jobs) != 0 {
		t.Error("o pacote saiu antes da hora")
	}
	pedidos, _ := st.WakeRequests(ctx, alvo.MachineID, 1)
	if len(pedidos) != 1 || len(pedidos[0].JobIDs) != 1 {
		t.Fatalf("pedido de ligar: %+v", pedidos)
	}
	wj, _, _ := st.JobForMachine(ctx, rep.MachineID, pedidos[0].JobIDs[0])
	if wj.NotBefore == nil || !wj.NotBefore.Equal(em.Add(-10*time.Minute)) {
		t.Errorf("pacote: %v", wj.NotBefore)
	}
	b = pagina(h, "GET", l, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Reinício agendado (job") || !strings.Contains(b, em.Format("02/01 às 15:04")) || !strings.Contains(b, "10 minutos antes, por 1 repetidor") {
		t.Errorf("aviso:\n%s", b)
	}

	// Sem repetidor na sub-rede: o reinício é agendado, e o aviso diz que o ligar não.
	l = pede(longe.MachineID, form(em, "120", true))
	if !strings.Contains(l, "reinicio=agendado") || !strings.Contains(l, "wol=ligar-erro%3Drepetidor") {
		t.Fatalf("sem repetidor: %s", l)
	}
	if b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "O reinício foi agendado, mas o ligar pela rede não") {
		t.Errorf("aviso sem repetidor:\n%s", b)
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("id %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
