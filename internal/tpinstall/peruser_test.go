package tpinstall

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// O programa por usuário (per_user): o plano da máquina (campanhas) não o instala, nem com
// instalação de máquina achada; a instalação nova como SYSTEM não sai (ForNewInstall), e a da
// Central sai (ForUserInstall).
func TestProgramaPorUsuario(t *testing.T) {
	canva, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Canva", WingetID: "Canva.Canva", InventoryName: "Canva", NameMode: thirdparty.NamePrefix,
		InstallType: "nullsoft", Signer: "Canva Pty Ltd"})
	if err != nil {
		t.Fatal(err)
	}
	canva.Install.PerUser = true
	m, err := thirdparty.ParseCustom([]thirdparty.App{canva})
	if err != nil {
		t.Fatal(err)
	}
	canva = m.Apps[0]
	pk := pacote(canva.ID, "1.120.0", "nullsoft", "x64")
	pk.Scope = "user"
	pkgs := []store.ThirdPartyPackage{pk}
	sw := []protocol.Software{maquina("Canva 1.100.0", "1.100.0", "Canva", usuario), maquina("Canva 1.90.0", "1.90.0", "Canva")}
	ds := Plan(sw, m, pkgs, "x64")
	if len(ds) != 1 || ds[0].State != StateUserOnly || ds[0].Package != nil {
		t.Fatalf("plano: %+v", ds)
	}
	if ForNewInstall(canva, pkgs, "x64") != nil {
		t.Error("ForNewInstall do por usuário")
	}
	if p := ForUserInstall(canva, pkgs, "x64"); p == nil || p.Version != "1.120.0" {
		t.Errorf("ForUserInstall: %+v", p)
	}
	canva.Install.PerUser = false
	if ForUserInstall(canva, pkgs, "x64") != nil {
		t.Error("ForUserInstall do de máquina")
	}
	// per_user com uninstall_first não passa.
	canva.Install.PerUser, canva.Install.UninstallFirst = true, true
	if _, err := thirdparty.ParseCustom([]thirdparty.App{canva}); err == nil {
		t.Error("per_user com uninstall_first passou")
	}
}
