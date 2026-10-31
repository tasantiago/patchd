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

// TestConfiguracoesDaBIOS (docs/bios-config.md): a aba BIOS lista tudo, só a lista fechada
// ganha seletor, só o que mudou vira job, risco alto pede confirmação, e a senha em uso vai
// fechada para a caixa da máquina, nunca legível no job nem na trilha.
func TestConfiguracoesDaBIOS(t *testing.T) {
	ctx := context.Background()
	const id = "60ef97e4-0001" // a máquina que o compliance de teste conhece
	const maq = "/painel/maquinas/" + id
	aba := maq + "?aba=bios"
	guardaPriv, _ := machinebox.Generate()
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, _ := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))

	sim, nao := true, false
	const seguro = "Configure Legacy Support and Secure Boot"
	settings := []protocol.BIOSSetting{
		{Name: "Wake On LAN", Path: `\Advanced\Power-On Options`, Kind: protocol.BIOSKindEnum, Value: "Disable",
			Possible: []string{"Disable", "Boot to Network", "Boot to Hard Drive"}},
		{Name: "Fast Boot", Path: `\Advanced\Boot Options`, Kind: protocol.BIOSKindEnum, Value: "Enable", Possible: []string{"Disable", "Enable"}},
		{Name: seguro, Path: `\Advanced\Secure Boot Configuration`, Kind: protocol.BIOSKindEnum, Value: "Legacy Support Disable and Secure Boot Enable",
			Possible: []string{"Legacy Support Enable and Secure Boot Disable", "Legacy Support Disable and Secure Boot Enable", "Legacy Support Disable and Secure Boot Disable"}},
		{Name: "TPM State", Kind: protocol.BIOSKindEnum, Value: "Enable", Possible: []string{"Disable", "Enable"}},
		{Name: "Asset Tracking Number", Kind: protocol.BIOSKindString, Value: "PAT-0001"},
	}
	reportSequence := 0
	firmware := func(fw protocol.Firmware) {
		t.Helper()
		reportSequence++ // único também em relógios Windows de baixa resolução
		_, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "cfg" + strconv.Itoa(reportSequence),
			OS: protocol.OSInfo{Family: "windows", Hostname: "pc-lab-02"}, Hardware: &protocol.Hardware{Manufacturer: "HP", Sources: []string{}, Firmware: &fw}})
		if err != nil {
			t.Fatal(err)
		}
	}
	e := estadoTeste()
	e.BoxKey = machinebox.EncodePublic(caixaPub)
	if err := st.SaveHostState(ctx, id, e, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	// form: os três editáveis (n0..n2) com os valores atuais, trocando os pedidos.
	form := func(motivo string, troca map[string]string, extra ...string) url.Values {
		f := url.Values{"motivo": {motivo}}
		for i, s := range settings[:3] {
			k := string(rune('0' + i))
			f.Set("n"+k, s.Name)
			f.Set("v"+k, s.Value)
			if v, ok := troca[s.Name]; ok {
				f.Set("v"+k, v)
			}
		}
		for i := 0; i+1 < len(extra); i += 2 {
			f.Set(extra[i], extra[i+1])
		}
		return f
	}
	pede := func(f url.Values) string {
		return local(pagina(h, "POST", maq+"/bios/configurar", sessaoAdmin, f))
	}
	ultimoJob := func() (store.JobRow, jobs.BIOSSettingsParams) {
		t.Helper()
		l, _ := st.Jobs(ctx, id, 1)
		j, _, _ := st.JobForMachine(ctx, id, l[0].ID)
		var payload jobs.Job
		_ = json.Unmarshal([]byte(j.Payload), &payload)
		p, err := jobs.ParseBIOSSettingsParams(payload.Params)
		if err != nil {
			t.Fatal(err)
		}
		return j, p
	}

	// Sem as configurações no inventário: o motivo, nada de formulário.
	firmware(protocol.Firmware{Mode: protocol.FirmwareUEFI})
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não mostra a interface do fabricante") || strings.Contains(b, "/bios/configurar") {
		t.Error("sem interface: o motivo, sem o formulário")
	}
	firmware(protocol.Firmware{Interface: "hp", AdminPassword: &nao, SureAdmin: &nao})
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não trouxe as configurações") {
		t.Error("agente antigo: o motivo")
	}

	// BIOS sem senha: os seletores das editáveis; TPM e o texto ficam na lista só leitura.
	firmware(protocol.Firmware{Interface: "hp", AdminPassword: &nao, SureAdmin: &nao, Settings: settings})
	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	for _, want := range []string{`action="/painel/maquinas/` + id + `/bios/configurar"`, `name="v0"`, `name="v2"`, "Energia e Wake on LAN",
		"risco alto", `name="confirmo"`, "As outras 2 configurações", "TPM State", "PAT-0001", "5 lidas · 3 alteráveis"} {
		if !strings.Contains(b, want) {
			t.Errorf("aba BIOS sem %q", want)
		}
	}
	if strings.Contains(b, `name="v3"`) {
		t.Error("TPM com seletor")
	}
	if strings.Contains(b, `<option value="Legacy Support Enable and Secure Boot Disable"`) {
		t.Error("ligar o suporte legado não pode ser oferecido")
	}
	if b := pagina(h, "GET", aba, sessaoTeste, nil).Body.String(); strings.Contains(b, "/bios/configurar") || !strings.Contains(b, "Wake On LAN") {
		t.Error("leitura: vê as configurações, sem o formulário")
	}
	if r := pagina(h, "POST", maq+"/bios/configurar", sessaoTeste, form("chamado 1", map[string]string{"Fast Boot": "Disable"})); r.Code != http.StatusForbidden {
		t.Errorf("leitura altera: %d", r.Code)
	}

	// Recusas do formulário.
	erro := aba + "&bios-config-erro="
	for nome, c := range map[string]struct {
		f    url.Values
		quer string
	}{
		"sem motivo":       {form("", map[string]string{"Fast Boot": "Disable"}), "motivo"},
		"nada mudou":       {form("chamado 1", nil), "nada"},
		"valor de fora":    {form("chamado 1", map[string]string{"Fast Boot": "Turbo"}), "valor"},
		"legado":           {form("chamado 1", map[string]string{seguro: "Legacy Support Enable and Secure Boot Disable"}, "confirmo", "1"), "valor"},
		"nome que sumiu":   {form("chamado 1", nil, "n1", "Boot Turbo", "v1", "Enable"), "mudou"},
		"repetida":         {form("chamado 1", map[string]string{"Fast Boot": "Disable"}, "n0", "Fast Boot", "v0", "Disable"), "valor"},
		"TPM pelo atalho":  {form("chamado 1", nil, "n0", "TPM State", "v0", "Disable"), "valor"},
		"risco sem marcar": {form("chamado 1", map[string]string{seguro: "Legacy Support Disable and Secure Boot Disable"}), "confirmar"},
	} {
		if r := pede(c.f); r != erro+c.quer {
			t.Errorf("%s: %s", nome, r)
		}
	}
	if l, _ := st.Jobs(ctx, id, 10); len(l) != 0 {
		t.Fatalf("recusas criaram jobs: %+v", l)
	}

	// Pedido válido: só o que mudou, sem senha.
	r := pede(form("chamado 4512", map[string]string{"Wake On LAN": "Boot to Hard Drive", seguro: "Legacy Support Disable and Secure Boot Disable"}, "confirmo", "1"))
	j1, p1 := ultimoJob()
	if r != aba+"&bios-config=pedida&job="+itoa(j1.ID) || j1.Type != jobs.TypeBIOSSettings || j1.CreatedBy != "thyago" {
		t.Fatalf("pedido: %s %+v", r, j1)
	}
	if p1.Vendor != "hp" || p1.BoxKey != machinebox.KeyID(caixaPub) || p1.Password != "" || len(p1.Changes) != 2 ||
		p1.Changes[0].Name != "Wake On LAN" || p1.Changes[1].Value != "Legacy Support Disable and Secure Boot Disable" {
		t.Errorf("parâmetros: %+v", p1)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.config.job"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "Wake On LAN: Disable → Boot to Hard Drive") || !strings.Contains(ev[0].Detail, "chamado 4512") ||
		!strings.Contains(ev[0].Detail, "BIOS sem senha") {
		t.Errorf("trilha: %+v", ev)
	}
	// Um job aberto trava o próximo.
	if r := pede(form("chamado 4513", map[string]string{"Fast Boot": "Disable"})); r != erro+"bloqueada" {
		t.Errorf("com job aberto: %s", r)
	}
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "há uma alteração da BIOS em andamento (job "+itoa(j1.ID)) ||
		!strings.Contains(b, "Wake On LAN → Boot to Hard Drive") {
		t.Error("job aberto: o motivo e a lista de pedidos")
	}
	_, _ = st.FinishJob(ctx, j1.ID, jobs.StateDone, "Wake On LAN: gravada", time.Now())

	// BIOS com senha: sem a senha em uso na guarda, não dá.
	firmware(protocol.Firmware{Interface: "hp", AdminPassword: &sim, SureAdmin: &nao, Settings: settings})
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "a guarda não tem a senha em uso") || strings.Contains(b, `name="v0"`) {
		t.Error("sem senha na guarda: o motivo, sem os seletores")
	}
	if r := pede(form("chamado 4514", map[string]string{"Fast Boot": "Disable"})); r != erro+"bloqueada" {
		t.Errorf("sem senha na guarda: %s", r)
	}
	// Grava uma senha pela guarda (fluxo da Aula 21.5) e conclui: vira a ativa.
	pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil)
	gl, _ := st.BIOSPasswords(ctx, id)
	pagina(h, "POST", maq+"/bios/"+itoa(gl[0].ID)+"/gravar", sessaoAdmin, url.Values{"senha_atual": {"Frota#2024"}})
	jp, _ := st.Jobs(ctx, id, 1)
	_, _ = st.FinishJob(ctx, jp[0].ID, jobs.StateDone, "trocada", time.Now())
	senha, err := biospw.Open(guardaPriv, id, gl[0].Escrow)
	if err != nil {
		t.Fatal(err)
	}

	r = pede(form("chamado 4515", map[string]string{"Fast Boot": "Disable"}))
	j2, p2 := ultimoJob()
	if r != aba+"&bios-config=pedida&job="+itoa(j2.ID) || len(p2.Changes) != 1 {
		t.Fatalf("com senha: %s %+v", r, p2)
	}
	raw, _ := base64.StdEncoding.DecodeString(p2.Password)
	pt, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSConfig, raw)
	if err != nil || string(pt) != senha {
		t.Fatalf("envelope da senha: %v", err)
	}
	if _, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSCurrent, raw); err == nil {
		t.Error("o envelope abre com o rótulo da troca de senha")
	}
	ev, _ = st.AuditEvents(ctx, store.AuditFilter{Action: "bios.config.job"})
	if strings.Contains(j2.Payload, senha) || strings.Contains(ev[0].Detail, senha) || !strings.Contains(ev[0].Detail, "senha da guarda #"+itoa(gl[0].ID)) {
		t.Errorf("senha legível ou trilha sem a origem: %+v", ev[0])
	}

	_, _ = st.FinishJob(ctx, j2.ID, jobs.StateDone, "Fast Boot: gravada", time.Now())

	// Dell (docs/bios-config.md, seção 8): os nomes da Dell, o job com a interface "dell".
	dell := []protocol.BIOSSetting{
		{Name: "WakeOnLan", Kind: protocol.BIOSKindEnum, Value: "Disabled", Possible: []string{"Disabled", "LanOnly", "LanWlan"}},
		{Name: "EmbNic1", Kind: protocol.BIOSKindEnum, Value: "EnabledPxe", Possible: []string{"Disabled", "Enabled", "EnabledPxe"}},
	}
	firmware(protocol.Firmware{Interface: "dell", AdminPassword: &sim, Settings: dell})
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="n0" value="WakeOnLan"`) || strings.Contains(b, `name="n1"`) || !strings.Contains(b, "2 lidas · 1 alteráveis") {
		t.Errorf("Dell: só o WakeOnLan editável (a placa cabeada não)")
	}
	r = local(pagina(h, "POST", maq+"/bios/configurar", sessaoAdmin, url.Values{"motivo": {"chamado 4516"}, "n0": {"WakeOnLan"}, "v0": {"LanOnly"}}))
	j3, p3 := ultimoJob()
	if r != aba+"&bios-config=pedida&job="+itoa(j3.ID) || p3.Vendor != "dell" || p3.Changes[0].Value != "LanOnly" || p3.Password == "" {
		t.Fatalf("Dell: %s %+v", r, p3)
	}
}
