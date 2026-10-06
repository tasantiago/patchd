package osv

import (
	"os"
	"strings"
	"testing"
	"time"
)

func lerRegistro(t *testing.T, id string) Record {
	t.Helper()
	f, err := os.Open("testdata/" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestParseModifiedIDs(t *testing.T) {
	csv := strings.Join([]string{
		"2026-10-05T07:30:02.615241916Z,UBUNTU-CVE-2020-27221",
		"2026-10-02T12:17:59.329313578Z,USN-8861-1",
		"",
		"2026-10-01T12:51:01Z,USN-8862-1",
		"2026-09-30T10:00:00Z,USN-8861-1", // repetida e mais antiga: fica a primeira
		"2026-09-29T08:00:00Z,LSN-0100-1",
	}, "\n")
	es, err := ParseModifiedIDs(strings.NewReader(csv), "USN-")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0] != (Entry{ID: "USN-8861-1", Modified: "2026-10-02T12:17:59.329313578Z"}) || es[1].ID != "USN-8862-1" {
		t.Fatalf("entradas: %+v", es)
	}

	todas, _ := ParseModifiedIDs(strings.NewReader(csv), "")
	if len(todas) != 4 {
		t.Errorf("sem prefixo: %+v", todas)
	}

	for _, ruim := range []string{"USN-1-1", "ontem,USN-1-1", "2026-10-01T12:51:01Z,"} {
		if _, err := ParseModifiedIDs(strings.NewReader(ruim), ""); err == nil {
			t.Errorf("linha %q deveria ser recusada", ruim)
		}
	}
}

func TestParseUSNReal(t *testing.T) {
	// openssl, só no 26.04: o caso cruzado com a VM do laboratório.
	r := lerRegistro(t, "USN-8861-1")
	if r.ID != "USN-8861-1" || r.Summary != "openssl vulnerabilities" || r.Unfixed != 0 || !r.Withdrawn.IsZero() {
		t.Fatalf("registro: %+v", r)
	}
	if !r.Published.Equal(time.Date(2026, 10, 1, 12, 2, 0, 0, time.UTC)) {
		t.Errorf("published: %v", r.Published)
	}
	if len(r.Fixes) != 1 || r.Fixes[0] != (Fix{Ecosystem: "Ubuntu:26.04:LTS", Package: "openssl", Version: "3.5.5-1ubuntu3.7"}) {
		t.Errorf("correções: %+v", r.Fixes)
	}
	if len(r.CVEs) != 2 || r.CVEs[0].Priority != "low" || !strings.HasPrefix(r.CVEs[0].CVSS, "CVSS:3.1/") {
		t.Errorf("CVEs: %+v", r.CVEs)
	}

	// libxpm: a época vem na versão e precisa ficar.
	r = lerRegistro(t, "USN-8862-1")
	var v2604 string
	for _, f := range r.Fixes {
		if f.Ecosystem == "Ubuntu:26.04:LTS" {
			v2604 = f.Version
		}
	}
	if len(r.Fixes) != 3 || v2604 != "1:3.5.17-1ubuntu0.26.04.2" {
		t.Errorf("libxpm: %+v", r.Fixes)
	}

	// gst-plugins-good: seis ecossistemas, três deles do Ubuntu Pro (ESM); o 16.04 Pro tem
	// três CVEs e os outros, quatro. As CVEs são por ecossistema.
	r = lerRegistro(t, "USN-8863-1")
	porEco := map[string]int{}
	for _, c := range r.CVEs {
		porEco[c.Ecosystem]++
	}
	if len(r.Fixes) != 6 || porEco["Ubuntu:Pro:16.04:LTS"] != 3 || porEco["Ubuntu:26.04:LTS"] != 4 {
		t.Errorf("gst: %d correções, CVEs por ecossistema %v", len(r.Fixes), porEco)
	}
}

func TestParseRegras(t *testing.T) {
	r, err := Parse(strings.NewReader(`{
	  "id": "USN-1-1", "published": "2026-01-01T00:00:00Z", "modified": "2026-01-02T00:00:00.123456789Z",
	  "withdrawn": "2026-01-03T00:00:00Z",
	  "affected": [
	    {"package": {"ecosystem": "Ubuntu:26.04:LTS", "name": "a"},
	     "ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}]}]},
	    {"package": {"ecosystem": "Ubuntu:26.04:LTS", "name": "b"},
	     "ranges": [{"type": "GIT", "events": [{"introduced": "0"}, {"fixed": "abc123"}]},
	                {"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "2.0-1"}]}],
	     "database_specific": {"cves_map": {"ecosystem": "Ubuntu:26.04:LTS",
	       "cves": [{"id": "CVE-9", "severity": [{"type": "Ubuntu", "score": "high"}]}]}}},
	    {"package": {"ecosystem": "Ubuntu:26.04:LTS", "name": "c"},
	     "ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "3.0-1"}]}],
	     "database_specific": {"cves_map": {"ecosystem": "Ubuntu:26.04:LTS",
	       "cves": [{"id": "CVE-9", "severity": [{"type": "Ubuntu", "score": "high"}]}]}}},
	    {"package": {"ecosystem": "", "name": "sem-ecossistema"}}
	  ]}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Withdrawn.IsZero() || r.Unfixed != 1 || len(r.Fixes) != 2 || r.Fixes[0].Version != "2.0-1" {
		t.Errorf("retirado, sem correção e GIT ignorado: %+v", r)
	}
	if len(r.CVEs) != 1 || r.CVEs[0].Priority != "high" || r.CVEs[0].CVSS != "" {
		t.Errorf("a CVE repetida no mesmo ecossistema conta uma vez: %+v", r.CVEs)
	}

	for _, ruim := range []string{`{}`, `não é json`, `{"id": "X", "published": "ontem"}`} {
		if _, err := Parse(strings.NewReader(ruim)); err == nil {
			t.Errorf("%s deveria ser recusado", ruim)
		}
	}
}
