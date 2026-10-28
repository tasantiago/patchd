package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoExclusao interface {
	contratoCampanhas
	SaveThirdPartyPackage(ctx context.Context, p ThirdPartyPackage) (bool, error)
	ThirdPartyPackages(ctx context.Context) ([]ThirdPartyPackage, error)
	ApproveThirdParty(ctx context.Context, a ThirdPartyApproval) (bool, error)
	RevokeThirdParty(ctx context.Context, app, version string) (bool, error)
	DeleteThirdPartyVersion(ctx context.Context, app, version string) ([]ThirdPartyPackage, error)
}

// Aula 23.3: excluir uma versão guardada. A aprovação, um job thirdparty em aberto com um dos
// instaladores e uma campanha não encerrada da versão seguram a exclusão.
func testarExclusaoDeVersao(t *testing.T, st contratoExclusao) {
	ctx := context.Background()
	sha := func(c string) string { return strings.Repeat(c, 64) }
	pk := func(ver, typ, s string) ThirdPartyPackage {
		return ThirdPartyPackage{AppID: "7zip", WingetID: "7zip.7zip", Version: ver, Architecture: "x64", Type: typ,
			URL: "https://www.7-zip.org/a/x.exe", SHA256: s, Size: 1700000}
	}
	for _, p := range []ThirdPartyPackage{pk("26.04", "exe", sha("a")), pk("26.04", "wix", sha("b")), pk("26.05", "exe", sha("c"))} {
		if _, err := st.SaveThirdPartyPackage(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := st.DeleteThirdPartyVersion(ctx, "7zip", "1.0"); err != nil || len(gone) != 0 {
		t.Fatalf("versão que não existe: %v %v", gone, err)
	}

	// Aprovada: recusa, e nada sai.
	if _, err := st.ApproveThirdParty(ctx, ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteThirdPartyVersion(ctx, "7zip", "26.04"); !errors.Is(err, ErrPackageApproved) {
		t.Fatalf("aprovada: %v", err)
	}
	if _, err := st.RevokeThirdParty(ctx, "7zip", "26.04"); err != nil {
		t.Fatal(err)
	}

	// Um job thirdparty em aberto com o MSI da versão: recusa; concluído, deixa de segurar.
	if _, err := st.SaveInventory(ctx, "excl-a", protocol.InventoryReport{SchemaVersion: 1, Hash: "excl-a"}); err != nil {
		t.Fatal(err)
	}
	jid, err := st.NextJobID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"id":1,"machine_id":"excl-a","type":"thirdparty","params":{"app":"7zip","version":"26.04","sha256":"` + sha("b") + `"}}`
	if err := st.CreateJob(ctx, NewJob{ID: jid, MachineID: "excl-a", Type: "thirdparty", Signed: protocol.SignedJob{Payload: payload, Signature: "s"},
		CreatedBy: "thyago", NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var inUse *PackageInUseError
	if _, err := st.DeleteThirdPartyVersion(ctx, "7zip", "26.04"); !errors.As(err, &inUse) || inUse.Jobs != 1 || inUse.Campaigns != 0 {
		t.Fatalf("job em aberto: %v", err)
	}
	// O job de outra versão não segura esta.
	if gone, err := st.DeleteThirdPartyVersion(ctx, "7zip", "26.05"); err != nil || len(gone) != 1 || gone[0].SHA256 != sha("c") {
		t.Fatalf("outra versão: %v %v", gone, err)
	}
	if _, err := st.FinishJob(ctx, jid, "concluido", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Campanha em andamento da versão: recusa; cancelada, deixa de segurar.
	cid, err := st.CreateCampaign(ctx, campaign.Campaign{Name: "7zip", Type: "thirdparty", App: "7zip", Version: "26.04", Rings: []int{0},
		Observe: time.Hour, TTL: 6 * time.Hour, CreatedBy: "thyago"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteThirdPartyVersion(ctx, "7zip", "26.04"); !errors.As(err, &inUse) || inUse.Jobs != 0 || inUse.Campaigns != 1 {
		t.Fatalf("campanha em andamento: %v", err)
	}
	c, _, err := st.Campaign(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	c.State = campaign.StateCanceled
	if ok, err := st.SaveCampaign(ctx, c, campaign.StateRunning); err != nil || !ok {
		t.Fatalf("cancelar: %t %v", ok, err)
	}

	gone, err := st.DeleteThirdPartyVersion(ctx, "7zip", "26.04")
	if err != nil || len(gone) != 2 {
		t.Fatalf("excluir: %v %v", gone, err)
	}
	if l, err := st.ThirdPartyPackages(ctx); err != nil || len(l) != 0 {
		t.Errorf("sobrou: %+v %v", l, err)
	}
}

func TestExclusaoDeVersaoMemoria(t *testing.T) { testarExclusaoDeVersao(t, NewMemory()) }

func TestExclusaoDeVersaoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarExclusaoDeVersao(t, NewPostgres(pool))
}
