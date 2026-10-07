package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/compliance"
)

func TestComplianceWindowsConsultas(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	ago := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	produtos := []msrc.Product{
		{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
		{ID: "12390", Name: "Windows 11 Version 24H2 for x64-based Systems"},
		{ID: "20853", Name: "Windows 11 version 26H1 for x64-based Systems"},
		{ID: "20900", Name: "Azure Linux 3.0 kernel"},
	}
	agosto := msrc.Document{ID: "2026-Aug", Title: "August", Initial: ago, Current: ago, Products: produtos,
		Vulns: []msrc.Vuln{{CVE: "CVE-A", Title: "Kernel"}, {CVE: "CVE-L", Title: "Linux"}, {CVE: "CVE-H", Title: "Hotpatch"}, {CVE: "CVE-N", Title: "Sem KB"}},
		Affected: []msrc.Affected{
			{CVE: "CVE-A", ProductID: "20438", Severity: "Important", BaseScore: 7.0},
			{CVE: "CVE-L", ProductID: "20900", Severity: "Important", BaseScore: 7.5}, // CVE do Azure Linux: não pode cair no Windows
			{CVE: "CVE-N", ProductID: "20438", Severity: "Low"},
		},
		Fixes: []msrc.Fix{
			{CVE: "CVE-A", ProductID: "20438", KB: "5121003", SubType: "Security Update", FixedBuild: "10.0.26200.9350"},
			{CVE: "CVE-A", ProductID: "20438", KB: "5129195", SubType: "Security Update", FixedBuild: "10.0.26200.9457"},
			// Correção sem linha de "afetado" para o produto: a CVE entra pela correção.
			{CVE: "CVE-H", ProductID: "20438", KB: "5129241", SubType: "SecurityHotpatchUpdate", FixedBuild: "10.0.26200.9448"},
		},
	}
	setembro := msrc.Document{ID: "2026-Sep", Title: "September", Initial: set, Current: set, Products: produtos,
		Vulns: []msrc.Vuln{{CVE: "CVE-A", Title: "Kernel (revisada)"}, {CVE: "CVE-S", Title: "Win32k", Exploited: true}},
		Affected: []msrc.Affected{
			{CVE: "CVE-A", ProductID: "20438", Severity: "Critical", BaseScore: 8.1},
			{CVE: "CVE-S", ProductID: "20438", Severity: "Important", BaseScore: 7.8},
			{CVE: "CVE-S", ProductID: "12390", Severity: "Important", BaseScore: 7.8},
		},
		Fixes: []msrc.Fix{
			{CVE: "CVE-S", ProductID: "20438", KB: "5124008", SubType: "Security Update", FixedBuild: "10.0.26200.9445"},
			{CVE: "CVE-S", ProductID: "12390", KB: "5124008", SubType: "Security Update", FixedBuild: "10.0.26100.9445"},
		},
	}
	for _, d := range []msrc.Document{agosto, setembro} {
		if err := st.SaveMSRCDocument(ctx, d, d.Current); err != nil {
			t.Fatal(err)
		}
	}
	cat := kev.Catalog{Version: "x", Released: "y", Vulns: []kev.Vuln{
		{CVE: "CVE-S", Vendor: "Microsoft", Product: "Windows", Added: set, CWEs: []string{}},
	}}
	if err := st.SaveKEV(ctx, cat); err != nil {
		t.Fatal(err)
	}

	prods, err := st.WindowsProducts(ctx)
	if err != nil || len(prods) != 3 || slices.ContainsFunc(prods, func(p compliance.MSRCProduct) bool { return p.ID == "20900" }) {
		t.Errorf("produtos: %+v %v", prods, err)
	}

	linhas, err := st.WindowsFixes(ctx, []string{"20438"})
	if err != nil {
		t.Fatal(err)
	}
	type chave struct{ doc, cve, kb string }
	got := map[chave]compliance.WindowsFix{}
	for _, l := range linhas {
		got[chave{l.Document, l.CVE, l.KB}] = l
	}
	want := []chave{
		{"2026-Aug", "CVE-A", "5121003"}, {"2026-Aug", "CVE-A", "5129195"}, {"2026-Sep", "CVE-A", ""},
		{"2026-Aug", "CVE-H", "5129241"}, {"2026-Aug", "CVE-N", ""}, {"2026-Sep", "CVE-S", "5124008"},
	}
	if len(linhas) != len(want) {
		t.Fatalf("linhas: %+v", linhas)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("faltou %+v em %+v", k, linhas)
		}
	}
	if s := got[chave{"2026-Sep", "CVE-S", "5124008"}]; !s.KEV || !s.Exploited || s.Build != "10.0.26200.9445" || s.Score != 7.8 || s.Severity != "Important" || !s.Released.Equal(set) {
		t.Errorf("CVE-S: %+v", s)
	}
	if a := got[chave{"2026-Sep", "CVE-A", ""}]; a.Severity != "Critical" || a.Title != "Kernel (revisada)" || a.KEV {
		t.Errorf("CVE-A revisada: %+v", a)
	}
	if h := got[chave{"2026-Aug", "CVE-H", "5129241"}]; h.Severity != "" || h.SubType != "SecurityHotpatchUpdate" {
		t.Errorf("CVE-H: %+v", h)
	}

	// De ponta a ponta: em agosto (.9350), a de setembro está pendente e a CVE-A foi relançada.
	r := compliance.EvaluateWindows(compliance.WindowsBuild{Major: 26200, UBR: 9350}, linhas)
	if r.CVEs != 4 || len(r.Pending) != 1 || r.Pending[0].CVE != "CVE-S" || len(r.Rereleased) != 1 || len(r.NoFix) != 2 || r.Hotpatch != 1 {
		t.Errorf("avaliação: %+v", r)
	}
}
