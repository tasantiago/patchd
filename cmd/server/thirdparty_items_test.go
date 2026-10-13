package main

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func TestItensDeTerceiros(t *testing.T) {
	agora := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ev := machineEval{Third: &thirdparty.Result{Findings: []thirdparty.Finding{
		{App: thirdparty.App{ID: "google-chrome", Name: "Google Chrome", Category: thirdparty.CategoryFeed},
			Software:  protocol.Software{Name: "Google Chrome", Version: "155.0.8059.40", Scope: "machine"},
			Installed: "155.0.8059.40", Reference: "156.0.8078.12", State: thirdparty.StateOutdated,
			Ref: &thirdparty.Ref{Source: "chrome:win64", Fetched: agora.Add(-10 * 24 * time.Hour)}},
		{App: thirdparty.App{ID: "vc2008", Name: "Visual C++ 2008", Category: thirdparty.CategoryEOL, Note: "fim do suporte em 2018"},
			Software: protocol.Software{Name: "VC 2008", Version: "9.0", Scope: "user"}, State: thirdparty.StateEOL},
	}}}
	got := thirdPartyItems(ev, agora)
	if len(got) != 2 {
		t.Fatalf("itens: %+v", got)
	}
	if c := got[0]; c.AppID != "google-chrome" || c.State != thirdparty.StateOutdated || c.Reference != "156.0.8078.12" ||
		c.Origin != "chrome:win64, 2026-10-03 (antiga)" {
		t.Errorf("chrome: %+v", c)
	}
	// Sem versão normalizada, vale a do inventário; a nota da regra vira a origem.
	if v := got[1]; v.Installed != "9.0" || v.Origin != "fim do suporte em 2018" || v.Scope != "user" {
		t.Errorf("vc2008: %+v", v)
	}
	if thirdPartyItems(machineEval{}, agora) != nil {
		t.Error("sem avaliação de terceiros: nil")
	}
}
