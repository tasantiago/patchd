package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// O terminal do administrador: desligado por padrão; ligado, só o admin vê a aba; a sessão
// pede o motivo; o comando vira job assinado, com a auditoria gravada antes; a saída que o
// agente manda aparece na aba e no histórico.
func TestTerminalDoAdministrador(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := jobs.GenerateKey()

	// Desligado: sem a aba, e os pedidos voltam com o aviso.
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5)
	m := registra(t, h, tok)
	maq := "/painel/maquinas/" + m.MachineID
	if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoAdmin, nil).Body.String(); strings.Contains(b, "aba=terminal") || !strings.Contains(b, "aba=tarefas") {
		t.Error("desligado: a aba aparece")
	}
	if l := local(pagina(h, "POST", maq+"/terminal/abrir", sessaoAdmin, url.Values{"motivo": {"chamado 123, conferir o spooler"}})); !strings.Contains(l, "terminal-erro=desligado") {
		t.Errorf("desligado: %s", l)
	}

	// A máquina registrada entra na avaliação de mentira, para a página dela abrir.
	conf := &complianceFalso{}
	h, st, _ = ambientePaginas(t, api.WithJobKey(priv), api.WithSettings(parametrosFixos{api.TerminalKey: "1"}), api.WithCompliance(conf))
	tok, hash = identity.NewSecret(identity.EnrollmentPrefix)
	st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5)
	m = registra(t, h, tok)
	conf.fleet.Machines = []protocol.ComplianceStatus{{MachineID: m.MachineID, Hostname: "pc-lab", State: "em_dia", Reasons: []string{},
		LastSeenAt: time.Now().UTC(), EvaluatedAt: time.Now().UTC()}}
	maq = "/painel/maquinas/" + m.MachineID
	checkinCom := func(caps ...string) {
		t.Helper()
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), Capabilities: caps})
		if r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo); r.Code != http.StatusOK {
			t.Fatalf("check-in: %d %s", r.Code, r.Body)
		}
	}
	checkinCom(protocol.CapabilityJobs)

	// Só o admin: a aba, as ações e o histórico.
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, "aba=terminal") {
		t.Error("leitura vê a aba")
	}
	if b := pagina(h, "GET", maq+"?aba=terminal", sessaoTeste, nil).Body.String(); strings.Contains(b, "Terminal do administrador") {
		t.Error("leitura abre a aba")
	}
	for _, p := range []string{"/terminal/abrir", "/terminal/comando", "/terminal/fechar"} {
		if r := pagina(h, "POST", maq+p, sessaoTeste, url.Values{"motivo": {"chamado 123, conferir"}}); r.Code != http.StatusForbidden {
			t.Errorf("leitura em %s: %d", p, r.Code)
		}
	}
	if r := pagina(h, "GET", "/painel/terminal", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura no histórico: %d", r.Code)
	}

	// Agente sem o terminal (anterior, ou -no-terminal): a aba explica, e o comando não sai.
	b := pagina(h, "GET", maq+"?aba=terminal", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "não aceita o terminal") || strings.Contains(b, `name="comando"`) {
		t.Errorf("agente sem o terminal:\n%s", b)
	}
	checkinCom(protocol.CapabilityJobs, protocol.CapabilityTerminal)

	// Sem sessão: pede o motivo; o comando sem sessão não sai.
	b = pagina(h, "GET", maq+"?aba=terminal", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="motivo"`) || strings.Contains(b, `name="comando"`) {
		t.Errorf("sem sessão:\n%s", b)
	}
	cmd := url.Values{"shell": {"powershell"}, "comando": {"Get-Service Spooler"}, "tempo": {"60"}}
	if l := local(pagina(h, "POST", maq+"/terminal/comando", sessaoAdmin, cmd)); !strings.Contains(l, "terminal-erro=sessao") {
		t.Errorf("sem sessão: %s", l)
	}
	if l := local(pagina(h, "POST", maq+"/terminal/abrir", sessaoAdmin, url.Values{"motivo": {"curto"}})); !strings.Contains(l, "terminal-erro=motivo") {
		t.Errorf("motivo curto: %s", l)
	}
	if l := local(pagina(h, "POST", maq+"/terminal/abrir", sessaoAdmin, url.Values{"motivo": {"chamado 123, conferir o spooler"}})); strings.Contains(l, "erro") {
		t.Fatalf("abrir: %s", l)
	}
	b = pagina(h, "GET", maq+"?aba=terminal", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="comando"`) || !strings.Contains(b, "Motivo: chamado 123, conferir o spooler") || !strings.Contains(b, ">PowerShell</option>") {
		t.Fatalf("sessão aberta:\n%s", b)
	}

	// Comandos inválidos não saem.
	for nome, f := range map[string]url.Values{
		"vazio":         {"shell": {"powershell"}, "comando": {"  "}, "tempo": {"60"}},
		"interpretador": {"shell": {"python"}, "comando": {"x"}, "tempo": {"60"}},
		"tempo":         {"shell": {"cmd"}, "comando": {"dir"}, "tempo": {"7"}},
	} {
		if l := local(pagina(h, "POST", maq+"/terminal/comando", sessaoAdmin, f)); !strings.Contains(l, "terminal-erro=comando") {
			t.Errorf("%s: %s", nome, l)
		}
	}
	if l, _ := st.Jobs(ctx, m.MachineID, 10); len(l) != 0 {
		t.Fatalf("job criado por comando inválido: %+v", l)
	}

	// O comando: job assinado, registro e auditoria com o texto.
	if l := local(pagina(h, "POST", maq+"/terminal/comando", sessaoAdmin, cmd)); strings.Contains(l, "erro") {
		t.Fatalf("comando: %s", l)
	}
	jl, _ := st.Jobs(ctx, m.MachineID, 10)
	if len(jl) != 1 || jl[0].Type != jobs.TypeCommand || jl[0].CreatedBy != "thyago" {
		t.Fatalf("job: %+v", jl)
	}
	j, _, _ := st.JobForMachine(ctx, m.MachineID, jl[0].ID)
	var env jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &env)
	if p, err := jobs.ParseCommandParams(env.Params); err != nil || p.Command != "Get-Service Spooler" || p.Shell != "powershell" || p.TimeoutSec != 60 {
		t.Errorf("parâmetros: %+v %v", p, err)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "terminal.comando"}); len(ev) != 1 || !strings.Contains(ev[0].Detail, "Get-Service Spooler") || ev[0].Target != m.MachineID {
		t.Errorf("trilha do comando: %+v", ev)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "terminal.abrir"}); len(ev) != 1 || !strings.Contains(ev[0].Detail, "chamado 123") {
		t.Errorf("trilha da sessão: %+v", ev)
	}
	b = pagina(h, "GET", maq+"?aba=terminal", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "esperando a máquina") || !strings.Contains(b, `http-equiv="refresh" content="3"`) {
		t.Errorf("esperando: sem a atualização automática")
	}

	// O agente responde: a saída aparece na aba (escapada) e no histórico.
	zero := 0
	res, _ := json.Marshal(protocol.JobResult{Status: protocol.JobResultOK, Detail: "código de saída 0 em 1.2s", FinishedAt: time.Now().UTC(),
		Output: "Status   Name\nRunning  Spooler <b>", ExitCode: &zero})
	if r := envia(h, "POST", fmt.Sprintf("/api/v1/agent/jobs/%d/result", jl[0].ID), m.Credential, res); r.Code != http.StatusOK {
		t.Fatalf("resultado: %d %s", r.Code, r.Body)
	}
	b = pagina(h, "GET", maq+"?aba=terminal", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Running  Spooler &lt;b&gt;", "código 0", "Get-Service Spooler", "PS&gt;"} {
		if !strings.Contains(b, quer) {
			t.Errorf("aba depois do resultado: falta %q", quer)
		}
	}
	if strings.Contains(b, `http-equiv="refresh"`) {
		t.Error("continua atualizando sem nada pendente")
	}
	hist := pagina(h, "GET", "/painel/terminal?maquina="+m.MachineID, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Get-Service Spooler", "motivo: chamado 123, conferir o spooler", "Running  Spooler", "thyago"} {
		if !strings.Contains(hist, quer) {
			t.Errorf("histórico: falta %q", quer)
		}
	}
	if b := pagina(h, "GET", "/painel/terminal?usuario=ninguem", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Nenhum comando neste filtro") {
		t.Error("filtro do histórico")
	}

	// Fechar a sessão: o próximo comando pede o motivo de novo.
	pagina(h, "POST", maq+"/terminal/fechar", sessaoAdmin, nil)
	if l := local(pagina(h, "POST", maq+"/terminal/comando", sessaoAdmin, cmd)); !strings.Contains(l, "terminal-erro=sessao") {
		t.Errorf("depois de fechar: %s", l)
	}
}
