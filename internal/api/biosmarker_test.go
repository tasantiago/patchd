package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// TestSenhaDoMarcadorNaPrimeiraTroca (docs/bios-senha.md, seção 11): uma HP com a senha que o
// .bat deixou no Registro. Com o campo em branco, o job manda o agente ler a senha na própria
// máquina (current_from), sem envelope de senha atual; a digitada continua valendo; fora da HP
// o marcador não é usado.
func TestSenhaDoMarcadorNaPrimeiraTroca(t *testing.T) {
	ctx := context.Background()
	const id = "60ef97e4-0001"
	const maq = "/painel/maquinas/" + id
	const hw = maq + "?aba=hardware"
	guardaPriv, _ := machinebox.Generate()
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, _ := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))
	sim := true
	maquina := func(iface string, marcador bool) {
		t.Helper()
		_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: iface + strconv.FormatBool(marcador),
			OS: protocol.OSInfo{Family: "windows", Hostname: "pc-lab-03"}, Hardware: &protocol.Hardware{Manufacturer: "HP", Sources: []string{},
				Firmware: &protocol.Firmware{Interface: iface, AdminPassword: &sim}}})
		e := estadoTeste()
		e.BoxKey = machinebox.EncodePublic(caixaPub)
		e.BIOSLegacy = marcador
		if err := st.SaveHostState(ctx, id, e, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	grava := func(senha string) string {
		t.Helper()
		gl, _ := st.BIOSPasswords(ctx, id)
		return local(pagina(h, "POST", maq+"/bios/"+itoa(gl[0].ID)+"/gravar", sessaoAdmin, url.Values{"senha_atual": {senha}}))
	}
	params := func() (jobs.BIOSPasswordParams, store.JobRow) {
		t.Helper()
		jl, _ := st.Jobs(ctx, id, 1)
		if len(jl) == 0 {
			t.Fatal("nenhum job")
		}
		j, _, _ := st.JobForMachine(ctx, id, jl[0].ID)
		var payload jobs.Job
		_ = json.Unmarshal([]byte(j.Payload), &payload)
		p, err := jobs.ParseBIOSPasswordParams(payload.Params)
		if err != nil {
			t.Fatal(err)
		}
		return p, j
	}

	maquina("hp", true)
	pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil)
	b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `placeholder="em branco: a do Registro (.bat)"`) || !strings.Contains(b, "o agente a lê na própria máquina") ||
		strings.Contains(b, `name="senha_atual" required`) {
		t.Errorf("cartão: o campo opcional e a explicação do marcador")
	}

	// Em branco: o agente lê no Registro. O job não leva senha atual nenhuma.
	if r := grava(""); !strings.HasPrefix(r, hw+"&bios=gravando&job=") {
		t.Fatalf("gravar pelo marcador: %s", r)
	}
	p, j := params()
	if p.CurrentFrom != jobs.BIOSCurrentMarker || p.Current != "" || p.Vendor != "hp" {
		t.Fatalf("job: %+v", p)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.gravar"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "a que o .bat deixou no Registro da máquina, lida pelo agente") {
		t.Errorf("trilha: %+v", ev)
	}

	// A BIOS recusou a do marcador: a digitada vale, fechada para a caixa.
	_, _ = st.FinishJob(ctx, j.ID, jobs.StateRejected, "recusado pelo agente: teste", time.Now())
	if r := grava("Digitada#1"); !strings.HasPrefix(r, hw+"&bios=gravando&job=") {
		t.Fatalf("gravar com a digitada: %s", r)
	}
	p, j = params()
	raw, _ := base64.StdEncoding.DecodeString(p.Current)
	if cur, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSCurrent, raw); err != nil || string(cur) != "Digitada#1" || p.CurrentFrom != "" {
		t.Fatalf("a digitada não foi no job: %+v %v", p, err)
	}

	// Sem o marcador (ou agente anterior): o campo volta a ser obrigatório.
	_, _ = st.FinishJob(ctx, j.ID, jobs.StateRejected, "recusado pelo agente: teste", time.Now())
	maquina("hp", false)
	if r := grava(""); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("sem marcador: %s", r)
	}
	// O .bat só rodou nas HP: numa Lenovo o aviso do agente não é usado.
	maquina("lenovo", true)
	if r := grava(""); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("lenovo com o aviso: %s", r)
	}
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); strings.Contains(b, "a do Registro (.bat)") {
		t.Error("lenovo: o cartão oferece o marcador")
	}
}

// TestSenhaDoMarcadorNaoEntraNoBanco: o agente anterior à correção manda a senha da BIOS como
// versão do programa BIOS_password e a cita na resposta da pergunta "programa"; o servidor
// descarta as duas, e a pergunta de registro não aceita a chave do marcador.
func TestSenhaDoMarcadorNaoEntraNoBanco(t *testing.T) {
	ctx := context.Background()
	const senha = "Exemplo@12345678"
	pub, priv, _ := jobs.GenerateKey()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)

	const uninstall = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	rep := protocol.InventoryReport{SchemaVersion: protocol.InventorySchemaVersion, AgentVersion: "v0.18.0-teste", CollectedAt: time.Now().UTC(),
		OS: protocol.OSInfo{Family: "windows", Name: "Windows 11", Hostname: "pc-hp"},
		Software: []protocol.Software{
			{Name: "7-Zip", Version: "24.08", Scope: "machine", Source: uninstall + `\7-Zip`},
			{Name: "BIOS_password", Version: senha, Scope: "machine", SystemComponent: true, Source: uninstall + `\BIOS_password`},
		}}
	rep.Hash, _ = rep.ContentHash()
	corpo, _ := json.Marshal(rep)
	if r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, corpo); r.Code != http.StatusCreated {
		t.Fatalf("inventário: %d %s", r.Code, r.Body)
	}
	inv, _, _ := st.LatestInventory(ctx, m.MachineID)
	if len(inv.Software) != 2 || inv.Software[0].Version != "24.08" || inv.Software[1].Name != "BIOS_password" || inv.Software[1].Version != "" {
		t.Fatalf("inventário guardado: %+v", inv.Software)
	}
	if !strings.Contains(log.String(), "senha da BIOS do marcador descartada") || strings.Contains(log.String(), senha) {
		t.Error("log: faltou o aviso, ou a senha foi junto")
	}
	if b := pagina(h, "GET", "/painel/maquinas/"+m.MachineID+"?aba=software", sessaoAdmin, nil).Body.String(); strings.Contains(b, senha) {
		t.Error("a senha aparece na aba Software")
	}

	// A pergunta de registro não lê a chave do marcador nem o histórico do .bat.
	for _, chave := range []string{uninstall + `\BIOS_password`, `HKLM\SOFTWARE\Exemplo\BIOS_password\Historico`} {
		l := local(pagina(h, "POST", "/painel/perguntas", sessaoAdmin, url.Values{"sensor": {"registro"}, "arg": {chave}, "nome": {"DisplayVersion"}, "alvo": {"todas"}}))
		if !strings.Contains(l, "erro=pergunta") {
			t.Errorf("pergunta de registro em %s: %s", chave, l)
		}
	}
	// A pergunta "programa" respondida por um agente antigo: a resposta fica retida.
	if l := local(pagina(h, "POST", "/painel/perguntas", sessaoAdmin, url.Values{"sensor": {"programa"}, "arg": {"bios"}, "alvo": {"todas"}})); l != "/painel/perguntas/1" {
		t.Fatalf("pergunta: %s", l)
	}
	env, _ := st.DeliverJobs(ctx, m.MachineID, time.Now())
	if len(env) != 1 {
		t.Fatalf("jobs entregues: %d", len(env))
	}
	j, err := jobs.Verify(pub, env[0], m.MachineID, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(jobs.QueryAnswer{Value: senha + ", 4.0", Extra: "BIOS_password " + senha + "; HP BIOS Config 4.0"})
	res, _ := json.Marshal(protocol.JobResult{Status: protocol.JobResultOK, Detail: string(resp), FinishedAt: time.Now().UTC()})
	if r := envia(h, "POST", protocol.JobResultPath+strconv.FormatInt(j.ID, 10)+"/result", m.Credential, res); r.Code >= 300 {
		t.Fatalf("resultado: %d %s", r.Code, r.Body)
	}
	row, _, _ := st.JobForMachine(ctx, m.MachineID, j.ID)
	if strings.Contains(row.Detail, senha) || !strings.Contains(row.Detail, "resposta retida") {
		t.Errorf("resposta guardada: %s", row.Detail)
	}
	if strings.Contains(log.String(), senha) {
		t.Error("a senha foi para o log do servidor")
	}
}
