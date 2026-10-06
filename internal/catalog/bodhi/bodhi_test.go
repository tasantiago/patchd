package bodhi

import (
	"os"
	"strings"
	"testing"
	"time"
)

func abrir(t *testing.T, nome string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + nome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParseReleases(t *testing.T) {
	p, err := ParseReleases(abrir(t, "releases-current.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fedora []string
	for _, r := range p.Releases {
		if r.IsFedora() {
			fedora = append(fedora, r.Name)
		}
	}
	if strings.Join(fedora, ",") != "F43,F44" || p.Pages != 1 {
		t.Errorf("só F43 e F44 (sem EPEL, Container e Flatpak): %v", fedora)
	}
}

func TestParseUpdates(t *testing.T) {
	p, err := ParseUpdates(abrir(t, "updates-F44.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Updates) != 3 || p.Total != 3 || p.SkippedBuilds != 2 {
		t.Fatalf("página: %d updates, total %d, %d builds pulados", len(p.Updates), p.Total, p.SkippedBuilds)
	}

	// O update real do chromium: títulos cortados, CVEs repetidas entre os bugs contam uma vez.
	c := p.Updates[0]
	if c.Alias != "FEDORA-2026-35b989661c" || c.Release != "F44" || c.Severity != "urgent" || !c.CVEsPartial {
		t.Errorf("chromium: %+v", c)
	}
	if !c.Pushed.Equal(time.Date(2026, 10, 5, 1, 5, 36, 0, time.UTC)) || !c.Stable.Equal(c.Pushed) {
		t.Errorf("datas (UTC): %v %v", c.Pushed, c.Stable)
	}
	if len(c.CVEs) != 27 || c.CVEs[0] != "CVE-2026-102299" {
		t.Errorf("CVEs do chromium (13 + 14, sem repetir): %d %v", len(c.CVEs), c.CVEs[:2])
	}
	if len(c.Builds) != 1 || c.Builds[0] != (Build{NVR: "chromium-154.0.8037.92-1.fc44", Name: "chromium", Version: "154.0.8037.92", Release: "1.fc44"}) {
		t.Errorf("build: %+v", c.Builds)
	}

	// Época 1 e época null (vale 0); nome com hífen; o bug que não é de segurança não conta.
	o := p.Updates[1]
	if len(o.Builds) != 2 || o.Builds[0].Epoch != 1 || o.Builds[1].Epoch != 0 || o.Builds[1].Name != "python-cryptography" {
		t.Errorf("builds: %+v", o.Builds)
	}
	if len(o.CVEs) != 1 || o.CVEs[0] != "CVE-2026-42772" || o.CVEsPartial {
		t.Errorf("CVEs: %v parcial=%t", o.CVEs, o.CVEsPartial)
	}

	// Flatpak e NVR fora do formato ficam de fora; datas null viram zero.
	f := p.Updates[2]
	if len(f.Builds) != 0 || !f.Pushed.IsZero() || !f.Stable.IsZero() {
		t.Errorf("flatpak: %+v", f)
	}

	for _, ruim := range []string{
		`não é json`,
		`{"updates": [{"title": "sem alias"}]}`,
		`{"updates": [{"alias": "X", "date_pushed": "05/10/2026"}]}`,
	} {
		if _, err := ParseUpdates(strings.NewReader(ruim)); err == nil {
			t.Errorf("%s deveria ser recusado", ruim)
		}
	}
}

func TestSplitNVR(t *testing.T) {
	casos := map[string][3]string{
		"chromium-154.0.8037.92-1.fc44":     {"chromium", "154.0.8037.92", "1.fc44"},
		"python-cryptography-45.0.7-2.fc44": {"python-cryptography", "45.0.7", "2.fc44"},
		"kernel-7.0.4-200.fc44":             {"kernel", "7.0.4", "200.fc44"},
	}
	for nvr, want := range casos {
		n, v, r, ok := SplitNVR(nvr)
		if !ok || [3]string{n, v, r} != want {
			t.Errorf("%s: %s %s %s %t", nvr, n, v, r, ok)
		}
	}
	for _, ruim := range []string{"", "sem-hifen", "-1-2", "a--2", "a-1-"} {
		if _, _, _, ok := SplitNVR(ruim); ok {
			t.Errorf("%q deveria ser recusado", ruim)
		}
	}
}
