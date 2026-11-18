package selfservice

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// A categoria do item na loja: a escolhida na oferta, ou a padrão do programa.
func TestCatalogoCategoria(t *testing.T) {
	m, _ := thirdparty.Default()
	maq := Machine{Arch: "x64", Software: []protocol.Software{}}
	pkgs := []store.ThirdPartyPackage{pacote("7zip", "26.04", "exe"), pacote("vscode", "1.106.0", "inno")}
	aprov := []store.ThirdPartyApproval{{AppID: "7zip", Version: "26.04"}, {AppID: "vscode", Version: "1.106.0"}}
	ofertas := []store.SelfServiceOffer{{AppID: "7zip", CanInstall: true}, {AppID: "vscode", CanInstall: true, Category: "sistemas"}}
	got := map[string]string{}
	for _, it := range Catalog(ofertas, ListMember(nil), maq, m, pkgs, aprov) {
		got[it.AppID] = it.Category
	}
	if got["7zip"] != "utilitarios" || got["vscode"] != "sistemas" {
		t.Errorf("categorias: %v", got)
	}
}
