package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/thirdparty"
)

// A origem de cada programa no painel conta o que as fontes novas dizem: o ciclo sem suporte
// (com a data), o fim próximo, a mais nova da linha, e o fabricante à frente do winget-pkgs.
func TestOrigemDosTerceiros(t *testing.T) {
	agora := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	d := func(s string) *time.Time {
		x, _ := time.Parse("2006-01-02", s)
		return &x
	}
	ref := &thirdparty.Ref{Source: "winget:Notepad++.Notepad++", Version: "8.9.8.1", Fetched: agora,
		CheckVersion: "8.9.9", CheckSource: "evergreen:NotepadPlusPlus"}
	ev := machineEval{Third: &thirdparty.Result{Findings: []thirdparty.Finding{
		{App: thirdparty.App{ID: "libreoffice", Category: thirdparty.CategoryLifecycle}, State: thirdparty.StateEOL,
			Cycle: &thirdparty.Cycle{Name: "25.2", EOLFrom: d("2026-03-08"), Fetched: agora}},
		{App: thirdparty.App{ID: "python", Category: thirdparty.CategoryLifecycle}, State: thirdparty.StateOutdated, EOLSoon: true,
			Cycle: &thirdparty.Cycle{Name: "3.11", EOLFrom: d("2027-01-31"), Fetched: agora}},
		{App: thirdparty.App{ID: "notepad-plus-plus", Category: thirdparty.CategoryFeed}, State: thirdparty.StateOutdated, Ref: ref, Behind: true},
	}}}
	items := thirdPartyItems(ev, agora)
	for i, want := range []string{
		"a mais nova do ciclo 25.2 (endoflife.date, 2026-11-16); o ciclo 25.2 está sem suporte desde 08/03/2026 (endoflife.date)",
		"a mais nova do ciclo 3.11 (endoflife.date, 2026-11-16); o suporte do ciclo 3.11 termina em 31/01/2027 (endoflife.date)",
		"evergreen:NotepadPlusPlus, 2026-11-16 (o fabricante já publicou; winget:Notepad++.Notepad++ ainda está em 8.9.8.1)",
	} {
		if !strings.Contains(items[i].Origin, want) {
			t.Errorf("%s: %q\nqueria %q", items[i].AppID, items[i].Origin, want)
		}
	}
}
