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
	if Labels[StateUpdate] != "ATUALIZAR" || len(Labels) != len(States) {
		t.Errorf("rótulos: %v", Labels)
	}
}
