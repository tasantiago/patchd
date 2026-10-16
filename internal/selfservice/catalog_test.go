package selfservice

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func pacote(app, ver, typ string) store.ThirdPartyPackage {
	return store.ThirdPartyPackage{AppID: app, Version: ver, Type: typ, Architecture: "x64", SHA256: strings.Repeat("a", 64)}
}

func resumo(l []Item) string {
	var s []string
	for _, it := range l {
		s = append(s, it.AppID+":"+it.Action+":"+it.Installed+">"+it.Target)
	}
	return strings.Join(s, " ")
}

// Aula 17.1: a loja junta a oferta, a aprovação e o plano da máquina.
func TestCatalogo(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	maq := Machine{Arch: "x64", Software: []protocol.Software{
		{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"},
		{Name: "Notepad++ (64-bit x64)", Version: "8.9.8.1", Publisher: "Notepad++ Team", Scope: "machine"},
	}}
	pkgs := []store.ThirdPartyPackage{
		pacote("7zip", "26.04", "exe"), pacote("7zip", "26.05", "exe"), // 26.05 guardado, mas não aprovado
		pacote("notepad-plus-plus", "8.9.8.1", "nullsoft"),
		pacote("vscode", "1.106.0", "inno"),
	}
	aprov := []store.ThirdPartyApproval{{AppID: "7zip", Version: "26.04"}, {AppID: "notepad-plus-plus", Version: "8.9.8.1"},
		{AppID: "vscode", Version: "1.106.0"}}
	ofertas := []store.SelfServiceOffer{
		{AppID: "7zip", CanUpdate: true},
		{AppID: "notepad-plus-plus", CanUpdate: true},
		{AppID: "vscode", CanInstall: true, Groups: []string{"GG-Dev"}},
		{AppID: "google-chrome", CanInstall: true}, // nenhuma versão aprovada
		{AppID: "adobe-acrobat", CanUpdate: true},  // regra sem "install": fica fora
	}
	todos := Catalog(ofertas, ListMember([]string{"gg-dev"}), maq, m, pkgs, aprov)
	if got := resumo(todos); got != "7zip:atualizar:26.01>26.04 vscode:instalar:>1.106.0 google-chrome:indisponivel:> notepad-plus-plus:em_dia:8.9.8.1>" {
		t.Errorf("com o grupo:\n%s", got)
	}
	// Sem o grupo, o VS Code some; sem "atualizar" na oferta, o 7-Zip só aparece instalado.
	ofertas[0].CanUpdate, ofertas[0].CanInstall = false, true
	if got := resumo(Catalog(ofertas, ListMember(nil), maq, m, pkgs, aprov)); got != "google-chrome:indisponivel:> notepad-plus-plus:em_dia:8.9.8.1> 7zip:instalado:26.01>" {
		t.Errorf("sem o grupo:\n%s", got)
	}
	// Duas instalações visíveis: indisponível, com o motivo para o suporte.
	maq.Software = append(maq.Software, protocol.Software{Name: "7-Zip 24.09 (x64 edition)", Version: "24.09.00.0", Publisher: "Igor Pavlov",
		Scope: "machine", ProductCode: "{23170F69-40C1-2702-2409-000001000000}"})
	ofertas[0].CanUpdate = true
	for _, it := range Catalog(ofertas, ListMember(nil), maq, m, pkgs, aprov) {
		if it.AppID == "7zip" && (it.Action != ActionBlocked || !strings.Contains(it.Reason, "fale com o suporte (duplicado")) {
			t.Errorf("duplicado: %+v", it)
		}
	}
}
