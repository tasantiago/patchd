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
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// TestGravacaoDaSenhaDaBIOS (Aula 21.5): o job leva as senhas fechadas para a caixa da
// máquina; a primeira troca pede a senha da frota; a resposta decide a guarda; incerta trava
// novas gravações até alguém conferir.
func TestGravacaoDaSenhaDaBIOS(t *testing.T) {
	ctx := context.Background()
	const id = "60ef97e4-0001"
	const maq = "/painel/maquinas/" + id
	hw := maq + "?aba=hardware"
	guardaPriv, _ := machinebox.Generate()
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, _ := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))

	sim, nao := true, false
	comCopia := true // o agente guarda a cópia de emergência (docs/bios-senha.md, seção 9)
	firmware := func(fw protocol.Firmware) {
		t.Helper()
		_, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "fw" + time.Now().Format("150405.000000000"),
			OS: protocol.OSInfo{Family: "windows", Hostname: "pc-lab-01"}, Hardware: &protocol.Hardware{Manufacturer: "HP", Sources: []string{}, Firmware: &fw}})
		if err != nil {
			t.Fatal(err)
		}
	}
	caixa := func(pub []byte) {
		e := estadoTeste()
		if pub != nil {
			e.BoxKey = machinebox.EncodePublic(pub)
		}
		e.BIOSCopy = comCopia
		if err := st.SaveHostState(ctx, id, e, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	prepara := func() store.BIOSPassword {
		t.Helper()
		pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil)
		l, _ := st.BIOSPasswords(ctx, id)
		return l[0]
	}
	senhaDa := func(b store.BIOSPassword) string {
		pw, err := biospw.Open(guardaPriv, id, b.Escrow)
		if err != nil {
			t.Fatal(err)
		}
		return pw
	}
	ultimoJob := func() (store.JobRow, jobs.BIOSPasswordParams) {
		t.Helper()
		l, _ := st.Jobs(ctx, id, 1)
		j, _, _ := st.JobForMachine(ctx, id, l[0].ID)
		var payload jobs.Job
		_ = json.Unmarshal([]byte(j.Payload), &payload)
		p, err := jobs.ParseBIOSPasswordParams(payload.Params)
		if err != nil {
			t.Fatal(err)
		}
		return j, p
	}
	abre := func(purpose, b64 string) string {
		raw, _ := base64.StdEncoding.DecodeString(b64)
		pt, err := machinebox.Open(caixaPriv, purpose, raw)
		if err != nil {
			t.Fatalf("envelope %s: %v", purpose, err)
		}
		return string(pt)
	}
	gravar := func(gid int64, form url.Values) string {
		return local(pagina(h, "POST", maq+"/bios/"+itoa(gid)+"/gravar", sessaoAdmin, form))
	}

	// Bloqueios: sem a interface do fabricante, Sure Admin, sem a caixa.
	b1 := prepara()
	firmware(protocol.Firmware{Mode: protocol.FirmwareUEFI})
	caixa(caixaPub)
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Gravação indisponível: o inventário não mostra a interface do fabricante") ||
		strings.Contains(b, "/gravar") {
		t.Error("sem interface: o motivo, sem o botão")
	}
	if r := gravar(b1.ID, nil); r != hw+"&bios-erro=bloqueada" {
		t.Errorf("sem interface: %s", r)
	}
	firmware(protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &sim})
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "HP Sure Admin ligado") {
		t.Error("Sure Admin: o motivo")
	}
	firmware(protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &nao})
	caixa(nil)
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não informou a chave de cifragem") {
		t.Error("sem caixa: o motivo")
	}
	caixa(caixaPub)

	// Primeira troca: a BIOS tem a senha da frota, e a guarda não. O campo aparece; sem ele, recusa.
	b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="senha_atual"`) || !strings.Contains(b, "Gravar na BIOS") {
		t.Fatalf("primeira troca: o campo e o botão:\n%s", b)
	}
	if b := pagina(h, "GET", hw, sessaoTeste, nil).Body.String(); strings.Contains(b, "/gravar") {
		t.Error("leitura vê o Gravar")
	}
	if r := pagina(h, "POST", maq+"/bios/"+itoa(b1.ID)+"/gravar", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura grava: %d", r.Code)
	}
	if r := gravar(b1.ID, nil); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("sem a atual: %s", r)
	}
	if r := gravar(b1.ID, url.Values{"senha_atual": {"Frota\x1b[31m"}}); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("atual com controle: %s", r)
	}
	r := gravar(b1.ID, url.Values{"senha_atual": {"Frota#2024"}})
	j1, p := ultimoJob()
	if r != hw+"&bios=gravando&job="+itoa(j1.ID) || j1.Type != jobs.TypeBIOSPassword || j1.CreatedBy != "thyago" {
		t.Fatalf("gravar: %s %+v", r, j1)
	}
	if p.Guard != b1.ID || p.Vendor != "hp" || p.BoxKey != machinebox.KeyID(caixaPub) ||
		abre(machinebox.PurposeBIOS, p.New) != senhaDa(b1) || abre(machinebox.PurposeBIOSCurrent, p.Current) != "Frota#2024" {
		t.Errorf("parâmetros: %+v", p)
	}
	if strings.Contains(j1.Payload, senhaDa(b1)) || strings.Contains(j1.Payload, "Frota#2024") {
		t.Fatal("senha legível no job")
	}
	// A cópia de emergência para o Registro: a da guarda, que só a chave de guarda abre.
	if c, err := biospw.ParseCopy(p.Copy); err != nil || c.MachineID != id || c.Guard != b1.ID {
		t.Errorf("cópia: %q %v", p.Copy, err)
	} else if pw, err := c.Reveal(guardaPriv); err != nil || pw != senhaDa(b1) {
		t.Errorf("a cópia não revela a senha nova: %v", err)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.gravar"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "troca (senha atual informada pelo admin)") || strings.Contains(ev[0].Detail, "Frota#2024") {
		t.Errorf("trilha: %+v", ev)
	}
	if r := gravar(b1.ID, url.Values{"senha_atual": {"Frota#2024"}}); r != hw+"&bios-erro=nao-pendente" {
		t.Errorf("segundo pedido: %s", r)
	}
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "gravando: job "+itoa(j1.ID)) {
		t.Error("o estado gravando")
	}

	// A máquina recusou (Sure Admin ligado desde o inventário): a senha volta a pendente.
	_, _ = st.FinishJob(ctx, j1.ID, jobs.StateRejected, "recusado pelo agente: HP Sure Admin ligado", time.Now())
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "rejeitado: recusado pelo agente: HP Sure Admin ligado") ||
		!strings.Contains(b, "/bios/"+itoa(b1.ID)+"/gravar") {
		t.Error("recusa: de volta a pendente, com o motivo")
	}
	// Agora vai: concluído → ativa. Agente anterior à cópia de emergência: o job vai sem ela
	// (o agente recusaria o campo novo).
	comCopia = false
	caixa(caixaPub)
	gravar(b1.ID, url.Values{"senha_atual": {"Frota#2024"}})
	j2, p2 := ultimoJob()
	if p2.Copy != "" {
		t.Error("cópia para um agente que não a entende")
	}
	comCopia = true
	caixa(caixaPub)
	_, _ = st.FinishJob(ctx, j2.ID, jobs.StateDone, "o inventário mostra a senha de administrador definida; agente: trocada (hp, código 0)", time.Now())
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, ">ativa</span>") || !strings.Contains(b, "Preparar senha nova") {
		t.Errorf("ativa:\n%s", b)
	}

	// Segunda troca: a atual sai da guarda, sem campo.
	b2 := prepara()
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); strings.Contains(b, `name="senha_atual"`) {
		t.Error("com a ativa na guarda, o campo não aparece")
	}
	gravar(b2.ID, nil)
	j3, p3 := ultimoJob()
	if abre(machinebox.PurposeBIOSCurrent, p3.Current) != senhaDa(b1) || abre(machinebox.PurposeBIOS, p3.New) != senhaDa(b2) {
		t.Error("segunda troca: a atual é a ativa da guarda")
	}
	ev, _ = st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.gravar"})
	if !strings.Contains(ev[0].Detail, "a da guarda #"+itoa(b1.ID)) {
		t.Errorf("trilha da segunda: %+v", ev[0])
	}
	// Resultado incerto: trava as próximas gravações até alguém resolver.
	_, _ = st.FinishJob(ctx, j3.ID, jobs.StateFailed, "o agente informou falha: "+jobs.BIOSUncertain+"a chamada ao fabricante quebrou no meio", time.Now())
	b3 := prepara()
	if r := gravar(b3.ID, nil); r != hw+"&bios-erro=incerta" {
		t.Errorf("com incerta: %s", r)
	}
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "incerta: a gravação saiu") || !strings.Contains(b, `value="nova">A nova vale`) {
		t.Error("incerta: o estado e os botões")
	}
	if r := pagina(h, "POST", maq+"/bios/"+itoa(b2.ID)+"/resolver", sessaoAdmin, url.Values{"valor": {"talvez"}}); local(r) != hw+"&bios-erro=resolver" {
		t.Errorf("resolver sem valor: %s", local(r))
	}
	if r := pagina(h, "POST", maq+"/bios/"+itoa(b2.ID)+"/resolver", sessaoAdmin, url.Values{"valor": {"nova"}}); local(r) != hw+"&bios=resolvida" {
		t.Fatalf("resolver: %s", local(r))
	}
	if a, b, _ := pega(st, b1.ID, b2.ID); a.State != store.BIOSReplaced || b.State != store.BIOSActive {
		t.Errorf("resolvida: %s / %s", a.State, b.State)
	}
	// Falha clara: a anterior continua ativa.
	gravar(b3.ID, nil)
	j4, _ := ultimoJob()
	_, _ = st.FinishJob(ctx, j4.ID, jobs.StateFailed, "o agente informou falha: hp respondeu 6 (acesso negado...)", time.Now())
	pagina(h, "GET", hw, sessaoAdmin, nil)
	if a, b, _ := pega(st, b2.ID, b3.ID); a.State != store.BIOSActive || b.State != store.BIOSFailed {
		t.Errorf("falha: %s / %s", a.State, b.State)
	}
}

func pega(st *store.Memory, a, b int64) (store.BIOSPassword, store.BIOSPassword, error) {
	x, _, _ := st.BIOSPasswordByID(context.Background(), a)
	y, _, err := st.BIOSPasswordByID(context.Background(), b)
	return x, y, err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
