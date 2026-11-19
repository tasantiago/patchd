package tpinstall

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func fonte(chave string) func(*protocol.Software) {
	return func(s *protocol.Software) {
		s.Source = `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\` + chave
	}
}
func silencioso(m string) func(*protocol.Software) {
	return func(s *protocol.Software) { s.Uninstall = m }
}

// A regularização: com o parâmetro ligado, a instalação de outro tipo (o Chrome do exe, o
// PowerShell do bundle) vira atualização com o instalador aprovado, se ele é mais novo. O Chrome
// instala por cima (a regra pede); os outros removem antes, e só com o agente que sabe e a
// desinstalação silenciosa registrada.
func TestRegularizacao(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []store.ThirdPartyPackage{
		pacote("google-chrome", "157.0.8103.79", "wix", "x64"),
		pacote("powershell-7", "7.6.9.0", "wix", "x64"),
	}
	for i := range pkgs {
		pkgs[i].Size, pkgs[i].Arguments = 1<<20, "/quiet /norestart"
	}
	chromeExe := maquina("Google Chrome", "156.0.7000.1", "Google LLC", fonte("Google Chrome"))
	psBundle := maquina("PowerShell 7.6.6.0-x64", "7.6.6.0", "Microsoft Corporation", bundle, silencioso(protocol.UninstallQuiet),
		fonte("{11111111-2222-3333-4444-555555555555}"))
	psMSIoculto := maquina("PowerShell 7-x64", "7.6.6.0", "Microsoft Corporation", msi, oculto)
	sw := []protocol.Software{chromeExe, psBundle, psMSIoculto}
	plano := func(o Options) map[string]Decision {
		got := map[string]Decision{}
		for _, d := range PlanWith(sw, m, pkgs, "x64", o) {
			got[d.App.ID] = d
		}
		return got
	}

	// Desligada: como antes.
	got := plano(Options{})
	if got["google-chrome"].State != StateNoPackage || got["powershell-7"].State != StateBundle {
		t.Fatalf("desligada: %s %s", got["google-chrome"].State, got["powershell-7"].State)
	}
	// Ligada, agente sem a remoção: o Chrome regulariza por cima; o PowerShell espera o agente.
	got = plano(Options{Regularize: true})
	c := got["google-chrome"]
	if c.State != StateUpdate || c.Regularize == nil || !c.Regularize.Over || c.Package.Version != "157.0.8103.79" || !strings.Contains(c.Reason, "por cima") {
		t.Fatalf("chrome: %+v", c)
	}
	if p, err := JobParams(c); err != nil || p.Replace != nil || p.Installer != "wix" {
		t.Errorf("job do chrome: %+v %v", p, err)
	}
	if ps := got["powershell-7"]; ps.State != StateBundle || !strings.Contains(ps.Reason, "atualize o agente") {
		t.Errorf("powershell sem o agente: %s %q", ps.State, ps.Reason)
	}
	// Com o agente: o PowerShell remove o bundle e instala o MSI.
	got = plano(Options{Regularize: true, CanReplace: true})
	ps := got["powershell-7"]
	if ps.State != StateUpdate || ps.Regularize == nil || ps.Regularize.Over || ps.Regularize.From != KindBundle || !strings.Contains(ps.Reason, "remove a instalação BUNDLE") {
		t.Fatalf("powershell: %+v", ps)
	}
	p, err := JobParams(ps)
	if err != nil || p.Replace == nil || p.Replace.Method != "quiet" || p.Replace.Name != "PowerShell 7.6.6.0-x64" ||
		!strings.HasSuffix(p.Replace.Key, `\{11111111-2222-3333-4444-555555555555}`) || p.Replace.ProductCode != "" {
		t.Fatalf("job do powershell: %+v %v", p.Replace, err)
	}

	// Sem desinstalação silenciosa registrada: fica, com o motivo.
	sw[1] = maquina("PowerShell 7.6.6.0-x64", "7.6.6.0", "Microsoft Corporation", bundle, fonte("x"))
	if ps := plano(Options{Regularize: true, CanReplace: true})["powershell-7"]; ps.State != StateBundle || !strings.Contains(ps.Reason, "desinstalação silenciosa") {
		t.Errorf("sem desinstalação: %s %q", ps.State, ps.Reason)
	}
	// A instalada é mais nova que a aprovada: não rebaixa.
	sw[0] = maquina("Google Chrome", "158.0.8127.37", "Google LLC", fonte("Google Chrome"))
	if c := plano(Options{Regularize: true})["google-chrome"]; c.State != StateCurrent || c.Regularize != nil || !strings.Contains(c.Reason, "próxima versão aprovada") {
		t.Errorf("mais nova: %s %q", c.State, c.Reason)
	}
}

// Remover antes (Install.UninstallFirst): a atualização do mesmo tipo remove a instalação de
// agora e instala, com o agente que remove e a desinstalação silenciosa registrada; sem eles,
// vai por cima, com o motivo.
func TestRemoverAntesDeAtualizar(t *testing.T) {
	base, _ := thirdparty.Default()
	app, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "GlobalProtect", WingetID: "PaloAltoNetworks.GlobalProtect", InventoryName: "GlobalProtect",
		NameMode: thirdparty.NameEquals, Publisher: "Palo Alto Networks", InstallType: "msi", Signer: "Palo Alto Networks, Inc", UninstallFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(app)
	m, errs := thirdparty.WithCustom(base, []json.RawMessage{raw})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !NeedsReplace(m) || NeedsReplace(base) {
		t.Error("NeedsReplace")
	}
	pc := "{32093C1C-778C-4524-8142-608176F5E804}"
	gp := maquina("GlobalProtect", "6.2.8", "Palo Alto Networks", msi, silencioso(protocol.UninstallMSI), fonte(pc), func(s *protocol.Software) { s.ProductCode = pc })
	pkgs := []store.ThirdPartyPackage{pacote(app.ID, "6.3.3", "msi", "x64")}
	pkgs[0].Size, pkgs[0].Arguments = 1<<20, "/quiet /norestart"
	plano := func(o Options, sw protocol.Software) Decision {
		for _, d := range PlanWith([]protocol.Software{sw}, m, pkgs, "x64", o) {
			if d.App.ID == app.ID {
				return d
			}
		}
		t.Fatal("sem a decisão")
		return Decision{}
	}
	d := plano(Options{CanReplace: true}, gp)
	if d.State != StateUpdate || d.Regularize == nil || d.Regularize.Over || !strings.Contains(d.Reason, "remove a instalação de agora e instala") {
		t.Fatalf("com o agente: %+v", d)
	}
	p, err := JobParams(d)
	if err != nil || p.Replace == nil || p.Replace.Method != "msi" || p.Replace.ProductCode != pc || p.Replace.Name != "GlobalProtect" || p.Version != "6.3.3" {
		t.Fatalf("job: %+v %v", p.Replace, err)
	}
	if d := plano(Options{}, gp); d.State != StateUpdate || d.Regularize != nil || !strings.Contains(d.Reason, "atualize o agente") {
		t.Errorf("agente antigo: %+v", d)
	}
	semRemocao := maquina("GlobalProtect", "6.2.8", "Palo Alto Networks", msi, fonte(pc))
	if d := plano(Options{CanReplace: true}, semRemocao); d.Regularize != nil || !strings.Contains(d.Reason, "não tem desinstalação silenciosa") {
		t.Errorf("sem desinstalação: %+v", d)
	}
	// Em dia: nada a remover.
	emDia := maquina("GlobalProtect", "6.3.3", "Palo Alto Networks", msi, silencioso(protocol.UninstallMSI), fonte(pc))
	if d := plano(Options{CanReplace: true}, emDia); d.State != StateCurrent || d.Regularize != nil {
		t.Errorf("em dia: %+v", d)
	}
}
