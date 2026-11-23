package tpinstall

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func TestJobParams(t *testing.T) {
	m, _ := thirdparty.Default()
	sw := []protocol.Software{maquina("Mozilla Firefox (x64 pt-BR)", "156.0", "Mozilla")}
	pk := pacote("mozilla-firefox", "157.0.1", "nullsoft", "x64")
	pk.Size, pk.Arguments, pk.ReturnCodes = 70<<20, "/S /PreventRebootRequired=true", map[int]string{1602: "cancelledByUser"}
	ds := Plan(sw, m, []store.ThirdPartyPackage{pk}, "x64")
	p, err := JobParams(ds[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.App != "mozilla-firefox" || p.Version != "157.0.1" || p.Installer != "nullsoft" || p.Size != 70<<20 ||
		strings.Join(p.Args, "|") != "/S|/PreventRebootRequired=true" || p.ReturnCodes["1602"] != "cancelledByUser" ||
		p.Signer != "Mozilla Corporation" {
		t.Errorf("parâmetros: %+v", p)
	}
	// Em dia (ou qualquer outro estado): sem job.
	if _, err := JobParams(Plan(sw, m, nil, "x64")[0]); err == nil {
		t.Error("sem pacote virou job")
	}
	// Argumento com aspas não cabe no job.
	pk.Arguments = `/S /D="C:\x"`
	if _, err := JobParams(Plan(sw, m, []store.ThirdPartyPackage{pk}, "x64")[0]); err == nil || !strings.Contains(err.Error(), "não cabem no job") {
		t.Errorf("aspas: %v", err)
	}
	// Os processos da regra vão no job (Aula 14.6b).
	vsc := pacote("vscode", "1.141.0", "inno", "x64")
	vsc.Size, vsc.Arguments = 120<<20, "/SP- /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /mergetasks=!runcode"
	ds = Plan([]protocol.Software{maquina("Microsoft Visual Studio Code", "1.123.0", "Microsoft Corporation")}, m, []store.ThirdPartyPackage{vsc}, "x64")
	if p, err := JobParams(ds[0]); err != nil || strings.Join(p.Processes, ",") != "Code.exe" || p.Signer != "Microsoft Corporation" {
		t.Errorf("vscode: %+v %v", p, err)
	}
	if Labels[StateUpdate] != "ATUALIZAR" || len(Labels) != len(States) {
		t.Errorf("rótulos: %v", Labels)
	}
}

// Tentar com o programa aberto: o job leva try_open só quando a regra pede (WithTryOpen) e o
// agente sabe (Options.CanTryOpen); sem uma das duas, o job vai como sempre (adia).
func TestJobTentarAberto(t *testing.T) {
	base, _ := thirdparty.Default()
	vsc := pacote("vscode", "1.141.0", "inno", "x64")
	vsc.Size, vsc.Arguments = 120<<20, "/VERYSILENT"
	sw := []protocol.Software{maquina("Microsoft Visual Studio Code", "1.123.0", "Microsoft Corporation")}
	pks := []store.ThirdPartyPackage{vsc}
	m := thirdparty.WithTryOpen(base, map[string]bool{"vscode": true, "7zip": true})
	if !NeedsCaps(m) || NeedsCaps(base) && !NeedsReplace(base) {
		t.Error("NeedsCaps")
	}
	for _, tc := range []struct {
		m    thirdparty.Manifest
		can  bool
		want bool
	}{{m, true, true}, {m, false, false}, {base, true, false}} {
		p, err := JobParams(PlanWith(sw, tc.m, pks, "x64", Options{CanTryOpen: tc.can})[0])
		if err != nil || p.TryOpen != tc.want {
			t.Errorf("regra %t, agente %t: try_open %t %v", tc.m.Apps[0].ID != "", tc.can, p.TryOpen, err)
		}
	}
	// O manifesto de entrada não muda; o programa sem processos não liga.
	for _, a := range base.Apps {
		if a.Install != nil && a.Install.TryOpen {
			t.Errorf("a base mudou: %s", a.ID)
		}
	}
	for _, a := range m.Apps {
		if a.ID == "7zip" && a.Install != nil && len(a.Install.Processes) == 0 && a.Install.TryOpen {
			t.Error("7zip sem processos ligou")
		}
	}
}
