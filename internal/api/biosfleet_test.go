package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestSenhaPadraoDaBIOS (docs/bios-senha.md, seção 10): a senha padrão da Lenovo é cadastrada
// uma vez em Configuração › BIOS, cifrada; a primeira troca de uma Lenovo com senha usa-a com
// o campo em branco; a senha nunca aparece no banco, na trilha nem na página.
func TestSenhaPadraoDaBIOS(t *testing.T) {
	ctx := context.Background()
	const id = "60ef97e4-0001"
	const maq = "/painel/maquinas/" + id
	const cfg = "/painel/configuracao?aba=bios"
	const padrao = "Frota#2024!"
	guardaPriv, _ := machinebox.Generate()
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, _ := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))
	salva := func(sessao string, f url.Values) *httptest.ResponseRecorder {
		return pagina(h, "POST", "/painel/configuracao/bios/senha-padrao", sessao, f)
	}
	form := func(fab, senha, conf, motivo string) url.Values {
		return url.Values{"fabricante": {fab}, "senha": {senha}, "confirmacao": {conf}, "motivo": {motivo}}
	}

	b := pagina(h, "GET", cfg, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Senha padrão da BIOS") || !strings.Contains(b, `action="/painel/configuracao/bios/senha-padrao"`) || !strings.Contains(b, "não cadastrada") {
		t.Fatalf("aba BIOS:\n%s", b)
	}
	if r := salva(sessaoTeste, form("lenovo", padrao, padrao, "chamado 1")); r.Code != http.StatusForbidden {
		t.Errorf("leitura grava: %d", r.Code)
	}
	erro := cfg + "&bios-padrao-erro="
	for nome, c := range map[string]struct {
		f    url.Values
		quer string
	}{
		"sem motivo":        {form("lenovo", padrao, padrao, ""), "motivo"},
		"asus":              {form("asus", padrao, padrao, "chamado 1"), "fabricante"},
		"confirmação":       {form("lenovo", padrao, "outra", "chamado 1"), "confirmacao"},
		"vírgula na lenovo": {form("lenovo", "a,b", "a,b", "chamado 1"), "senha&fabricante=lenovo"},
		"acento na lenovo":  {form("lenovo", "senhaçã", "senhaçã", "chamado 1"), "senha&fabricante=lenovo"},
		"vazia":             {form("dell", "", "", "chamado 1"), "senha&fabricante=dell"},
	} {
		if r := local(salva(sessaoAdmin, c.f)); r != erro+c.quer {
			t.Errorf("%s: %s", nome, r)
		}
	}
	if r := local(salva(sessaoAdmin, form("lenovo", padrao, padrao, "chamado 4600"))); r != cfg+"&bios-padrao=gravada&fabricante=lenovo" {
		t.Fatalf("gravar: %s", r)
	}
	l, _ := st.BIOSFleetPasswords(ctx)
	if len(l) != 1 || l[0].Vendor != "lenovo" || l[0].SetBy != "thyago" || strings.Contains(string(l[0].Escrow), padrao) {
		t.Fatalf("banco: %+v", l)
	}
	if pw, err := biospw.OpenFleet(guardaPriv, "lenovo", l[0].Escrow); err != nil || pw != padrao {
		t.Fatalf("envelope: %v", err)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.padrao"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "chamado 4600") || strings.Contains(ev[0].Detail, padrao) {
		t.Errorf("trilha: %+v", ev)
	}
	if b := pagina(h, "GET", cfg, sessaoAdmin, nil).Body.String(); strings.Contains(b, padrao) || !strings.Contains(b, "cadastrada em") {
		t.Error("a página mostra a senha, ou não mostra que está cadastrada")
	}

	// Uma Lenovo com a senha padrão e sem senha na guarda: o campo pode ficar em branco.
	sim := true
	_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "lenovo",
		OS: protocol.OSInfo{Family: "windows", Hostname: "pc-lab-03"}, Hardware: &protocol.Hardware{Manufacturer: "LENOVO", Sources: []string{},
			Firmware: &protocol.Firmware{Interface: "lenovo", AdminPassword: &sim}}})
	e := estadoTeste()
	e.BoxKey = machinebox.EncodePublic(caixaPub)
	_ = st.SaveHostState(ctx, id, e, "192.0.2.1")
	pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil)
	gl, _ := st.BIOSPasswords(ctx, id)
	hw := maq + "?aba=hardware"
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "em branco: a padrão da Lenovo") || !strings.Contains(b, "usa a senha padrão da Lenovo") {
		t.Errorf("cartão: o campo opcional e o aviso")
	}
	// Digitada com vírgula na Lenovo: recusa no painel, antes do job.
	if r := local(pagina(h, "POST", maq+"/bios/"+itoa(gl[0].ID)+"/gravar", sessaoAdmin, url.Values{"senha_atual": {"a,b"}})); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("vírgula digitada: %s", r)
	}
	r := local(pagina(h, "POST", maq+"/bios/"+itoa(gl[0].ID)+"/gravar", sessaoAdmin, url.Values{"senha_atual": {""}}))
	jl, _ := st.Jobs(ctx, id, 1)
	if len(jl) == 0 || r != hw+"&bios=gravando&job="+itoa(jl[0].ID) {
		t.Fatalf("gravar com a padrão: %s", r)
	}
	j, _, _ := st.JobForMachine(ctx, id, jl[0].ID)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	p, err := jobs.ParseBIOSPasswordParams(payload.Params)
	if err != nil || p.Vendor != "lenovo" {
		t.Fatalf("job: %+v %v", p, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(p.Current)
	if cur, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSCurrent, raw); err != nil || string(cur) != padrao {
		t.Fatalf("a atual do job não é a padrão: %v", err)
	}
	if strings.Contains(j.Payload, padrao) {
		t.Fatal("senha padrão legível no job")
	}
	ev, _ = st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.gravar"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "a padrão da Lenovo, cadastrada por thyago") {
		t.Errorf("trilha da troca: %+v", ev)
	}

	// Removida: a primeira troca volta a pedir a senha digitada.
	_, _ = st.FinishJob(ctx, j.ID, jobs.StateRejected, "recusado pelo agente: teste", time.Now())
	if r := local(pagina(h, "POST", "/painel/configuracao/bios/senha-padrao/remover", sessaoAdmin, url.Values{"fabricante": {"lenovo"}, "motivo": {"chamado 4601"}})); r != cfg+"&bios-padrao=removida&fabricante=lenovo" {
		t.Fatalf("remover: %s", r)
	}
	if r := local(pagina(h, "POST", maq+"/bios/"+itoa(gl[0].ID)+"/gravar", sessaoAdmin, url.Values{"senha_atual": {""}})); r != hw+"&bios-erro=senha-atual" {
		t.Errorf("sem a padrão: %s", r)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.padrao.remover"}); len(ev) != 1 {
		t.Errorf("trilha da remoção: %+v", ev)
	}
}
