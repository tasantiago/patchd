package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

func documentoDeTeste(id string, inicial time.Time) msrc.Document {
	return msrc.Document{
		ID: id, Title: "Security Updates " + id, Initial: inicial, Current: inicial,
		Products: []msrc.Product{
			{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
			{ID: "11", Name: "Microsoft Office LTSC 2024"},
		},
		Vulns: []msrc.Vuln{
			{CVE: "CVE-1", Title: "Win32k", Exploited: true, Exploit: "Publicly Disclosed:No;Exploited:Yes"},
			{CVE: "CVE-2", Title: "Kernel"},
			{CVE: "CVE-2", Title: "Kernel (repetida)"},
			{CVE: "CVE-3", Title: "Office"},
		},
		Affected: []msrc.Affected{
			{CVE: "CVE-1", ProductID: "20438", Severity: "Important", Impact: "Elevation of Privilege", BaseScore: 7.8, Vector: "CVSS:3.1/AV:L"},
			{CVE: "CVE-2", ProductID: "20438", Severity: "Critical", Impact: "Remote Code Execution", BaseScore: 9.8},
			{CVE: "CVE-3", ProductID: "11", Severity: "Important"},
		},
		Fixes: []msrc.Fix{
			// 10001 > 9445 como número; como texto, "9445" ganharia.
			{CVE: "CVE-1", ProductID: "20438", KB: "5124008", SubType: "Security Update", FixedBuild: "10.0.26200.9445"},
			{CVE: "CVE-2", ProductID: "20438", KB: "5125000", SubType: "Security Update", FixedBuild: "10.0.26200.10001"},
			{CVE: "CVE-2", ProductID: "20438", KB: "5125000", SubType: "Security Update", FixedBuild: "10.0.26200.10001"},
			{CVE: "CVE-3", ProductID: "11", KB: "5002900", SubType: "Security Update"},
		},
	}
}

func TestCatalogoMSRC(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	ago := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	rev1 := time.Date(2026, 10, 2, 14, 29, 13, 0, time.UTC)
	if err := st.SaveMSRCDocument(ctx, documentoDeTeste("2026-Aug", ago), rev1); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMSRCDocument(ctx, documentoDeTeste("2026-Sep", set), set); err != nil {
		t.Fatal(err)
	}
	v, err := st.MSRCVersions(ctx)
	if err != nil || !v["2026-Aug"].Equal(rev1) || !v["2026-Sep"].Equal(set) {
		t.Fatalf("versões: %v %v", v, err)
	}

	docs, err := st.MSRCDocuments(ctx)
	if err != nil || len(docs) != 2 || docs[0].ID != "2026-Sep" {
		t.Fatalf("documentos (mais novo primeiro): %+v %v", docs, err)
	}
	if d := docs[0]; d.Vulns != 3 || d.Exploited != 1 || d.Fixes != 3 {
		t.Errorf("contagens (repetições descartadas): %+v", d)
	}

	// A revisão substitui o documento inteiro: a CVE-1 sai, e a data da lista muda.
	rev2 := time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)
	novo := documentoDeTeste("2026-Sep", set)
	novo.Vulns = novo.Vulns[1:]
	novo.Affected = novo.Affected[1:]
	novo.Fixes = novo.Fixes[1:]
	if err := st.SaveMSRCDocument(ctx, novo, rev2); err != nil {
		t.Fatal(err)
	}
	docs, _ = st.MSRCDocuments(ctx)
	if d := docs[0]; d.Vulns != 2 || d.Exploited != 0 || d.Fixes != 2 || !d.Current.Equal(rev2) {
		t.Errorf("depois da revisão: %+v", d)
	}

	// Prefixo sem diferenciar maiúsculas: o MSRC escreve "Version" e "version".
	builds, err := st.MSRCBuilds(ctx, "windows 11")
	if err != nil || len(builds) != 2 {
		t.Fatalf("builds: %+v %v", builds, err)
	}
	for _, b := range builds {
		if b.Product != "Windows 11 Version 25H2 for x64-based Systems" || b.Build != "10.0.26200.10001" || b.KBs != "5125000" {
			t.Errorf("maior build por comparação numérica: %+v", b)
		}
	}
	if builds[0].Document != "2026-Sep" || builds[1].Document != "2026-Aug" {
		t.Errorf("ordem (mais novo primeiro): %+v", builds)
	}
}
