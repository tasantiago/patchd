package kev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	f, err := os.Open("testdata/kev-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2026.10.04" || c.Released != "2026-10-04T18:52:56.0635Z" || len(c.Vulns) != 10 {
		t.Fatalf("cabeçalho: %+v (%d itens)", c, len(c.Vulns))
	}
	v := c.Vulns[0]
	if v.CVE != "CVE-2026-88779" || v.Vendor != "Citrix" || !v.Added.Equal(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)) ||
		!v.Due.Equal(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)) || v.Ransomware || !slices.Equal(v.CWEs, []string{"CWE-119"}) {
		t.Errorf("primeiro item: %+v", v)
	}
	ransom := 0
	for _, v := range c.Vulns {
		if v.Ransomware {
			ransom++
		}
	}
	if ransom != 1 {
		t.Errorf("ransomware: %d", ransom)
	}

	for nome, ruim := range map[string]string{
		"ilegível":       `não é json`,
		"sem versão":     `{"count": 1, "vulnerabilities": [{"cveID": "CVE-2026-1111", "dateAdded": "2026-10-01"}]}`,
		"count errado":   `{"catalogVersion": "x", "dateReleased": "y", "count": 2, "vulnerabilities": [{"cveID": "CVE-2026-1111", "dateAdded": "2026-10-01"}]}`,
		"vazio":          `{"catalogVersion": "x", "dateReleased": "y", "count": 0, "vulnerabilities": []}`,
		"ID ruim":        `{"catalogVersion": "x", "dateReleased": "y", "count": 1, "vulnerabilities": [{"cveID": "CVE-26-1", "dateAdded": "2026-10-01"}]}`,
		"data ruim":      `{"catalogVersion": "x", "dateReleased": "y", "count": 1, "vulnerabilities": [{"cveID": "CVE-2026-1111", "dateAdded": "01/10/2026"}]}`,
		"CVE repetida":   `{"catalogVersion": "x", "dateReleased": "y", "count": 2, "vulnerabilities": [{"cveID": "CVE-2026-1111", "dateAdded": "2026-10-01"}, {"cveID": "CVE-2026-1111", "dateAdded": "2026-10-01"}]}`,
		"prazo inválido": `{"catalogVersion": "x", "dateReleased": "y", "count": 1, "vulnerabilities": [{"cveID": "CVE-2026-1111", "dateAdded": "2026-10-01", "dueDate": "logo"}]}`,
	} {
		if _, err := Parse(strings.NewReader(ruim)); err == nil {
			t.Errorf("%s: deveria ser recusado", nome)
		}
	}
}

func TestFedoraPackages(t *testing.T) {
	casos := []struct {
		vendor, product string
		quer            []string
	}{
		{"Google", "Chromium V8", []string{"chromium"}},
		{"Google", "Chrome Blink", []string{"chromium"}},
		{"Google", "Chrome for Android UI", nil}, // Android não é o Chromium do Fedora
		{"Mozilla", "Firefox, Firefox ESR, and Thunderbird", []string{"firefox", "thunderbird"}},
		{"Mozilla", "Multiple Products", []string{"firefox", "thunderbird"}},
		{"Linux", "Kernel", []string{"kernel"}},
		{"Android", "Kernel", nil}, // o fabricante precisa bater exatamente
		{"Synacor", "Zimbra Collaboration Suite (ZCS)", nil},
		{"Exim", "Mail Transfer Agent (MTA)", []string{"exim"}},
	}
	for _, c := range casos {
		if got := FedoraPackages(c.vendor, c.product); !slices.Equal(got, c.quer) {
			t.Errorf("%s / %s: %v, esperado %v", c.vendor, c.product, got, c.quer)
		}
	}
}

func TestClient(t *testing.T) {
	recorte, err := os.ReadFile("testdata/kev-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/kev.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(recorte)
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	ctx := context.Background()

	c.URL = srv.URL + "/kev.json"
	if cat, err := c.Fetch(ctx); err != nil || len(cat.Vulns) != 10 {
		t.Fatalf("fetch: %d %v", len(cat.Vulns), err)
	}
	c.URL = srv.URL + "/nada"
	if _, err := c.Fetch(ctx); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	c.URL = "http://exemplo.invalid/kev.json"
	if _, err := c.Fetch(ctx); err == nil || !strings.Contains(err.Error(), "exige https") {
		t.Errorf("http fora de localhost: %v", err)
	}
	c.URL, c.MaxSize = srv.URL+"/kev.json", 10
	if _, err := c.Fetch(ctx); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite: %v", err)
	}
}
