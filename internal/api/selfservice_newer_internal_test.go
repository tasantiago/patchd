package api

import (
	"context"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

type complianceNewer struct{ d protocol.ComplianceDetail }

func (c complianceNewer) Fleet(context.Context) (protocol.ComplianceFleet, error) {
	return protocol.ComplianceFleet{}, nil
}
func (c complianceNewer) Detail(context.Context, string) (protocol.ComplianceDetail, bool, error) {
	return c.d, true, nil
}
func (c complianceNewer) Items(context.Context) ([]protocol.MachineItems, error) { return nil, nil }

// O programa em dia com a versão aprovada, que a avaliação diz desatualizado, leva a versão nova
// do fabricante para a Central; o que precisa de atualização (ou está em dia de fato) não muda.
func TestCatalogoMarcaVersaoNova(t *testing.T) {
	a := &api{compliance: complianceNewer{d: protocol.ComplianceDetail{ThirdParty: []protocol.ThirdPartyItem{
		{AppID: "google-chrome", State: "DESATUALIZADO", Installed: "158.0", Reference: "159.0"},
		{AppID: "7zip", State: "DESATUALIZADO", Installed: "26.01", Reference: "26.04"},
		{AppID: "firefox", State: "em dia", Installed: "161.0", Reference: "161.0"},
	}}}}
	cat := protocol.SelfServiceCatalog{Items: []protocol.SelfServiceItem{
		{App: "google-chrome", Action: "em_dia", Installed: "158.0"},
		{App: "7zip", Action: "atualizar", Installed: "26.01", Target: "26.04"},
		{App: "firefox", Action: "em_dia", Installed: "161.0"},
	}}
	if err := a.markNewer(context.Background(), "m1", &cat); err != nil {
		t.Fatal(err)
	}
	if cat.Items[0].Newer != "159.0" || cat.Items[1].Newer != "" || cat.Items[2].Newer != "" {
		t.Fatalf("itens: %+v", cat.Items)
	}
}
