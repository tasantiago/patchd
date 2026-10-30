package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// TestDesempenhoNoPainel (docs/monitoramento.md): as amostras do check-in abrem o alerta, a
// tela Alertas e o menu mostram, o admin reconhece, e a aba Desempenho desenha a curva.
func TestDesempenhoNoPainel(t *testing.T) {
	ctx := context.Background()
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	agora := time.Now().UTC().Truncate(time.Minute)
	var amostras []protocol.PerfSample
	for i := 12; i >= 1; i-- { // 12 minutos com a CPU em 97%: abre (padrão: 10 seguidos)
		amostras = append(amostras, protocol.PerfSample{At: agora.Add(-time.Duration(i) * time.Minute), CPU: 97, Mem: 40,
			Disks: map[string]float64{"C:": 35}})
	}
	checkinCom := func(perf []protocol.PerfSample, motivo string) protocol.CheckinResponse {
		t.Helper()
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v-teste", SentAt: time.Now().UTC(), Perf: perf, Reason: motivo})
		r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
		if r.Code != http.StatusOK {
			t.Fatalf("check-in: %d %s", r.Code, r.Body)
		}
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}
	resp := checkinCom(amostras, "desempenho")
	if resp.Monitor == nil || *resp.Monitor != protocol.DefaultMonitorPolicy {
		t.Fatalf("limites na resposta: %+v", resp.Monitor)
	}
	l, _ := st.PerfAlerts(ctx, store.PerfAlertFilter{OpenOnly: true})
	if len(l) != 1 || l[0].MachineID != m.MachineID || l[0].Metric != "cpu" {
		t.Fatalf("alerta: %+v", l)
	}
	// Lote fora do formato: descartado, o check-in passa.
	checkinCom([]protocol.PerfSample{{At: agora, CPU: 300}}, "")

	b := pagina(h, "GET", "/painel/alertas", sessaoAdmin, nil).Body.String()
	for _, want := range []string{"<code>" + m.MachineID[:8] + "</code>", ">CPU<", "97%", ">aberto<", `action="/painel/alertas/` + itoa(l[0].ID) + `/reconhecer"`,
		`title="alertas de desempenho abertos e não reconhecidos">1<`} {
		if !strings.Contains(b, want) {
			t.Errorf("tela Alertas sem %q", want)
		}
	}
	if b := pagina(h, "GET", "/painel/alertas", sessaoTeste, nil).Body.String(); strings.Contains(b, "/reconhecer") || !strings.Contains(b, m.MachineID[:8]) {
		t.Error("leitura: vê o alerta, sem o botão")
	}
	if r := pagina(h, "POST", "/painel/alertas/"+itoa(l[0].ID)+"/reconhecer", sessaoTeste, url.Values{"nota": {"x"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura reconhece: %d", r.Code)
	}
	if r := local(pagina(h, "POST", "/painel/alertas/"+itoa(l[0].ID)+"/reconhecer", sessaoAdmin, url.Values{"nota": {"chamado 4700"}})); r != "/painel/alertas?reconhecido="+itoa(l[0].ID) {
		t.Fatalf("reconhecer: %s", r)
	}
	b = pagina(h, "GET", "/painel/alertas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "reconhecido") || !strings.Contains(b, "chamado 4700") || strings.Contains(b, "/reconhecer\"") {
		t.Error("depois do reconhecimento: o estado, a nota e sem o botão")
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "alerta.reconhecer"}); len(ev) != 1 {
		t.Errorf("trilha: %+v", ev)
	}

	// A aba Desempenho (na máquina que o compliance de teste conhece): valores atuais, a curva.
	const conhecida = "60ef97e4-0001"
	if _, err := st.SavePerf(ctx, conhecida, amostras, protocol.DefaultMonitorPolicy, agora); err != nil {
		t.Fatal(err)
	}
	b = pagina(h, "GET", "/painel/maquinas/"+conhecida+"?aba=desempenho", sessaoAdmin, nil).Body.String()
	for _, want := range []string{`<svg class="grafico"`, `<polyline class="l-cpu" points="`, `<polyline class="l-disco"`, "35% livre", "por minuto",
		">aberto<", "Alertas desta máquina"} {
		if !strings.Contains(b, want) {
			t.Errorf("aba Desempenho sem %q", want)
		}
	}
	if b := pagina(h, "GET", "/painel/maquinas/"+conhecida+"?aba=desempenho&periodo=7d", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Sem medidas neste período") && !strings.Contains(b, "média por hora") {
		t.Error("7 dias: sem as horas montadas, o aviso")
	}
}
