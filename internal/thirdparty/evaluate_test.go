package thirdparty

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// As versões de referência das fontes em 2026-10-07 (Aula 6.5, parte 1, e o winget).
func refsDoLab() []Ref {
	quando := time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC)
	r := func(app, os, v, src string) Ref {
		return Ref{App: app, OS: os, Version: v, Source: src, Fetched: quando}
	}
	return []Ref{
		r("google-chrome", "windows", "155.0.8059.40", "chrome:win64"),
		r("mozilla-firefox", "windows", "157.0.1", "firefox:LATEST_FIREFOX_VERSION"),
		r("mozilla-firefox-esr", "windows", "140.17.0", "firefox:FIREFOX_ESR"),
		r("7zip", "windows", "26.04", "winget:7zip.7zip"),
		r("adobe-acrobat", "windows", "26.002.21996", "winget:Adobe.Acrobat.Reader.64-bit"),
		r("notepad-plus-plus", "windows", "8.9.8.1", "winget:Notepad++.Notepad++"),
		r("vscode", "windows", "1.140.0", "winget:Microsoft.VisualStudioCode"),
		r("powershell-7", "windows", "7.6.6.0", "winget:Microsoft.PowerShell"),
		r("python-3.14", "windows", "3.14.7", "winget:Python.Python.3.14"),
		r("libreoffice", "windows", "26.8.0.3", "winget:TheDocumentFoundation.LibreOffice"),
		r("google-chrome", "macos", "155.0.8059.40", "chrome:mac"),
	}
}

func inventarioDoLab(t *testing.T) []protocol.Software {
	t.Helper()
	data, err := os.ReadFile("testdata/inventario-windows.json")
	if err != nil {
		t.Fatal(err)
	}
	var sw []protocol.Software
	if err := json.Unmarshal(data, &sw); err != nil {
		t.Fatal(err)
	}
	return sw
}

func TestEvaluateWindows(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	r := Evaluate("windows", inventarioDoLab(t), m, refsDoLab())
	if r.Programs != 24 || len(r.Findings) != 18 || r.Auxiliary != 1 || len(r.Unmatched) != 5 {
		t.Fatalf("contagens: %d programas, %d identificados, %d auxiliares, %d sem regra", r.Programs, len(r.Findings), r.Auxiliary, len(r.Unmatched))
	}
	for estado, n := range map[string]int{StateOutdated: 9, StateEOL: 5, StateOSVendor: 3, StateCurrent: 1, StateBadVersion: 0} {
		if got := r.Count(estado); got != n {
			t.Errorf("%s: %d, queria %d", estado, got, n)
		}
	}
	por := map[string]Finding{}
	for _, f := range r.Findings {
		por[f.Software.Name] = f
	}
	// As duas instalações do 7-Zip, cada uma com a sua versão.
	if a, b := por["7-Zip 26.02 (x64)"], por["7-Zip 26.02 (x64 edition)"]; a.Installed != "26.02" || b.Installed != "26.02.00.0" || a.State != StateOutdated || b.State != StateOutdated {
		t.Errorf("7-Zip: %+v / %+v", a, b)
	}
	// Python: a versão vem do nome, não do "3.14-64" do registro.
	if p := por["Python 3.14.3"]; p.Installed != "3.14.3" || p.Reference != "3.14.7" || p.State != StateOutdated {
		t.Errorf("Python: %+v", p)
	}
	if p := por["PowerShell 7.6.6.0-x64"]; p.State != StateCurrent || p.Ref == nil || p.Ref.Source != "winget:Microsoft.PowerShell" {
		t.Errorf("PowerShell: %+v", p)
	}
	if c := por["Google Chrome"]; c.Reference != "155.0.8059.40" || c.State != StateOutdated {
		t.Errorf("Chrome: %+v", c)
	}
	// Desatualizados primeiro, depois fora de suporte, catálogo do SO e em dia.
	if r.Findings[0].State != StateOutdated || r.Findings[len(r.Findings)-1].State != StateCurrent {
		t.Errorf("ordem: %s ... %s", r.Findings[0].State, r.Findings[len(r.Findings)-1].State)
	}
	var sem []string
	for _, s := range r.Unmatched {
		sem = append(sem, s.Name)
	}
	if !slices.Equal(sem, []string{"Burp Suite 2026.7.3", "Java 8 Update 503 (64-bit)", "Mesh Agent", "Microsoft Visual C++ v14 Redistributable (x64) - 14.50.35719", "Spotify"}) {
		t.Errorf("sem regra: %v", sem)
	}

	// Sem a referência guardada (catálogo nunca sincronizado), não há "em dia".
	r = Evaluate("windows", inventarioDoLab(t), m, nil)
	if r.Count(StateNoReference) != 10 || r.Count(StateCurrent) != 0 {
		t.Errorf("sem referências: %d sem referência, %d em dia", r.Count(StateNoReference), r.Count(StateCurrent))
	}
}

func TestEvaluateMinimoEMacOS(t *testing.T) {
	local, err := Parse([]byte(`{"version": 1, "apps": [
	  {"id": "java-8", "os": "windows", "category": "minimum", "publisher": "^Oracle", "match": "^Java 8 Update", "min_version": "8.0.5040"},
	  {"id": "mesh-agent", "os": "windows", "category": "minimum", "match": "^Mesh Agent$", "min_version": "1.0"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	padrao, _ := Default()
	r := Evaluate("windows", inventarioDoLab(t), padrao.Merge(local), refsDoLab())
	for _, f := range r.Findings {
		switch f.App.ID {
		case "java-8":
			if f.State != StateBelowMinimum || f.Installed != "8.0.5030.1" || f.Reference != "8.0.5040" {
				t.Errorf("Java: %+v", f)
			}
		case "mesh-agent":
			if f.State != StateBadVersion {
				t.Errorf("Mesh Agent (versão é uma data): %+v", f)
			}
		}
	}

	mac := []protocol.Software{
		{Name: "Google Chrome", Version: "155.0.8059.40", Publisher: "Google LLC"},
		{Name: "Firefox", Version: "156.0", Publisher: "Mozilla Corporation"},
		{Name: "Safari", Version: "26.0", Publisher: "Apple", SystemComponent: true},
		{Name: "Keynote", Version: "14.4", Publisher: "Apple"},
	}
	r = Evaluate("macos", mac, padrao, refsDoLab())
	if r.Programs != 3 || r.Count(StateCurrent) != 1 || r.Count(StateNoReference) != 1 || len(r.Unmatched) != 1 {
		t.Errorf("macOS: %+v", r)
	}
}
