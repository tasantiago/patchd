package msrc

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseUpdates(t *testing.T) {
	const lista = `{"@odata.context":"x","value":[
	 {"ID":"2026-Sep","Alias":"2026-Sep","DocumentTitle":"September 2026 Security Updates","Severity":null,
	  "InitialReleaseDate":"2026-09-08T07:00:00Z","CurrentReleaseDate":"2026-10-04T01:53:47Z",
	  "CvrfUrl":"https://api.msrc.microsoft.com/cvrf/v3.0/cvrf/2026-Sep"}]}`
	us, err := ParseUpdates(strings.NewReader(lista))
	if err != nil || len(us) != 1 {
		t.Fatalf("%+v %v", us, err)
	}
	u := us[0]
	if u.ID != "2026-Sep" || !u.CurrentReleaseDate.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) || !strings.HasSuffix(u.CvrfURL, "/2026-Sep") {
		t.Errorf("item: %+v", u)
	}
}

// Regras do parser num documento mínimo, no mesmo formato do real.
func TestParseRegras(t *testing.T) {
	const doc = `{"DocumentTitle":{"Value":"T"},
	 "DocumentTracking":{"Identification":{"ID":{"Value":"2026-Sep"}},"InitialReleaseDate":"2026-09-08T07:00:00","CurrentReleaseDate":"2026-10-04T01:53:47"},
	 "ProductTree":{"FullProductName":[{"ProductID":"1","Value":"Produto A"},{"ProductID":"2","Value":"Produto B"}]},
	 "Vulnerability":[{"CVE":"CVE-1","Title":{"Value":"Falha"},
	  "ProductStatuses":[{"ProductID":["1","2"]},{"ProductID":["1"]}],
	  "Threats":[{"Type":0,"Description":{"Value":"Remote Code Execution"},"ProductID":["1"]},
	             {"Type":3,"Description":{"Value":"Critical"},"ProductID":["1"]},
	             {"Type":3,"Description":{"Value":"Important"},"ProductID":["2"]},
	             {"Type":1,"Description":{"Value":"Publicly Disclosed:Yes;Exploited:Yes;Latest Software Release:Exploitation Detected"}}],
	  "CVSSScoreSets":[{"BaseScore":9.8,"Vector":"CVSS:3.1/AV:N","ProductID":["1"]}],
	  "Remediations":[{"Type":2,"SubType":"Security Update","Description":{"Value":"5124012"},"FixedBuild":"10.0.26200.9445","Supercedence":"5121000","RestartRequired":{"Value":"Yes"},"ProductID":["1","2"]},
	                  {"Type":2,"SubType":"Security Update","Description":{"Value":"Release Notes"},"FixedBuild":"","RestartRequired":{},"ProductID":["2"]},
	                  {"Type":3,"SubType":"5124012","ProductID":["1"]},
	                  {"Type":6,"Description":{"Value":"5124012"},"ProductID":["1"]}]}]}`
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "2026-Sep" || !d.Current.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) || len(d.Products) != 2 {
		t.Errorf("documento: %+v", d)
	}
	if len(d.Vulns) != 1 || !d.Vulns[0].Exploited || !d.Vulns[0].Disclosed {
		t.Errorf("exploração: %+v", d.Vulns)
	}
	// Um produto repetido nos ProductStatuses vira uma linha só; o CVSS é o do produto.
	if len(d.Affected) != 2 {
		t.Fatalf("afetados: %+v", d.Affected)
	}
	a1, a2 := d.Affected[0], d.Affected[1]
	if a1.ProductID != "1" || a1.Severity != "Critical" || a1.Impact != "Remote Code Execution" || a1.BaseScore != 9.8 {
		t.Errorf("produto 1: %+v", a1)
	}
	if a2.ProductID != "2" || a2.Severity != "Important" || a2.Impact != "" || a2.BaseScore != 0 {
		t.Errorf("produto 2 (sem impacto nem CVSS próprios): %+v", a2)
	}
	// Só o tipo 2 com número de KB vira correção: "Release Notes", tipo 3 e tipo 6 ficam de fora.
	if len(d.Fixes) != 2 || d.SkippedFix != 1 {
		t.Fatalf("correções: %+v (ignoradas %d)", d.Fixes, d.SkippedFix)
	}
	f := d.Fixes[0]
	if f.KB != "5124012" || f.FixedBuild != "10.0.26200.9445" || f.Supersedes != "5121000" || f.RestartRequired != "Yes" || f.SubType != "Security Update" {
		t.Errorf("correção: %+v", f)
	}
	if _, err := Parse(strings.NewReader(`{"DocumentTracking":{}}`)); err == nil {
		t.Error("documento sem ID deveria ser recusado")
	}
}

// O recorte do documento real de setembro de 2026 (scripts da Aula 6.1): os valores
// conferidos na saída da exploração precisam sair iguais do parser.
func TestRecorteReal(t *testing.T) {
	f, err := os.Open("testdata/2026-Sep-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "2026-Sep" || !d.Current.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) {
		t.Errorf("documento: %s %v", d.ID, d.Current)
	}

	nomes := map[string]string{}
	for _, p := range d.Products {
		nomes[p.ID] = p.Name
	}
	if nomes["20853"] != "Windows 11 version 26H1 for x64-based Systems" {
		t.Errorf("produto 20853: %q", nomes["20853"])
	}

	var achou bool
	for _, x := range d.Fixes {
		if x.CVE == "CVE-2026-50349" && x.ProductID == "20853" {
			achou = true
			if x.KB != "5124012" || x.FixedBuild != "10.0.28000.2954" || x.Supersedes != "5121000" ||
				x.RestartRequired != "Yes" || x.SubType != "Security Update" {
				t.Errorf("correção da CVE-2026-50349 no 26H1 x64: %+v", x)
			}
		}
	}
	if !achou {
		t.Error("falta a correção da CVE-2026-50349 no produto 20853")
	}
	for _, a := range d.Affected {
		if a.CVE == "CVE-2026-50349" && a.ProductID == "20853" &&
			(a.Severity != "Important" || a.Impact != "Elevation of Privilege" || !strings.HasPrefix(a.Vector, "CVSS:3.1/")) {
			t.Errorf("CVE-2026-50349 no 26H1 x64: %+v", a)
		}
	}

	var explorada, build25H2 bool
	for _, v := range d.Vulns {
		explorada = explorada || v.Exploited
	}
	for _, x := range d.Fixes {
		build25H2 = build25H2 || (x.ProductID == "20438" && strings.HasPrefix(x.FixedBuild, "10.0.26200."))
		if x.KB == "" {
			t.Errorf("correção sem KB: %+v", x)
		}
	}
	if !explorada || !build25H2 || d.SkippedFix == 0 {
		t.Errorf("o recorte traz uma CVE explorada (%t), um build do 25H2 (%t) e uma correção sem KB ignorada (%d)",
			explorada, build25H2, d.SkippedFix)
	}
}

// O documento inteiro, baixado à parte: PATCHD_MSRC_DOC=/tmp/msrc-sep.json go test -v -run Completo
func TestDocumentoCompleto(t *testing.T) {
	path := os.Getenv("PATCHD_MSRC_DOC")
	if path == "" {
		t.Skip("defina PATCHD_MSRC_DOC com o caminho de um documento baixado")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inicio := time.Now()
	d, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	explorada := 0
	for _, v := range d.Vulns {
		if v.Exploited {
			explorada++
		}
	}
	t.Logf("%s (%s): %d produtos, %d CVEs (%d exploradas), %d ligações CVE×produto, %d correções, %d sem KB ignoradas, em %s",
		d.ID, d.Current.Format(time.RFC3339), len(d.Products), len(d.Vulns), explorada, len(d.Affected), len(d.Fixes), d.SkippedFix,
		time.Since(inicio).Round(time.Millisecond))
	if len(d.Vulns) == 0 || len(d.Fixes) == 0 {
		t.Error("documento sem CVEs ou sem correções")
	}
}
