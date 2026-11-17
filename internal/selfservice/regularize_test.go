package selfservice

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// Com a regularização ligada, o Chrome instalado pelo exe aparece para atualizar para o MSI
// aprovado, marcado como troca da instalação; desligada, fica indisponível como antes.
func TestCatalogoRegularizacao(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	maq := Machine{Arch: "x64", Software: []protocol.Software{{Name: "Google Chrome", Version: "156.0.7000.1", Publisher: "Google LLC",
		Scope: "machine", Source: `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Google Chrome`}}}
	pkgs := []store.ThirdPartyPackage{pacote("google-chrome", "157.0.8103.79", "wix")}
	aprov := []store.ThirdPartyApproval{{AppID: "google-chrome", Version: "157.0.8103.79"}}
	ofertas := []store.SelfServiceOffer{{AppID: "google-chrome", CanUpdate: true}}

	if got := Catalog(ofertas, ListMember(nil), maq, m, pkgs, aprov); len(got) != 1 || got[0].Action != ActionBlocked || got[0].Replaces {
		t.Fatalf("desligada: %+v", got)
	}
	maq.Options = tpinstall.Options{Regularize: true}
	got := Catalog(ofertas, ListMember(nil), maq, m, pkgs, aprov)
	if len(got) != 1 || got[0].Action != ActionUpdate || got[0].Target != "157.0.8103.79" || !got[0].Replaces {
		t.Fatalf("ligada: %+v", got)
	}
}
