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

// Os ciclos de suporte do endoflife.date que o inventário do laboratório usa.
func ciclosDoLab() []Cycle {
	d := func(s string) *time.Time {
		t, _ := time.Parse("2006-01-02", s)
		return &t
	}
	return []Cycle{
		{Product: "powershell", Name: "7.6", EOLFrom: d("2028-11-14"), Latest: "7.6.6"},
		{Product: "powershell", Name: "7.4", EOLFrom: d("2026-11-10"), Latest: "7.4.13"},
		{Product: "libreoffice", Name: "25.2", EOLFrom: d("2026-03-08"), Latest: "25.2.7"},
		{Product: "libreoffice", Name: "26.8", EOLFrom: d("2027-06-13"), Latest: "26.8.3"},
		{Product: "python", Name: "3.14", EOLFrom: d("2030-10-31"), Latest: "3.14.10"},
		{Product: "notepad-plus-plus", Name: "8.9", Latest: "8.9.9.1"},
	}
}

func TestEvaluateWindows(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := EvaluateWith("windows", inventarioDoLab(t), m, Data{Refs: refsDoLab(), Cycles: ciclosDoLab(), Now: agora})
	if r.Programs != 24 || len(r.Findings) != 18 || r.Auxiliary != 1 || len(r.Unmatched) != 5 {
		t.Fatalf("contagens: %d programas, %d identificados, %d auxiliares, %d sem regra", r.Programs, len(r.Findings), r.Auxiliary, len(r.Unmatched))
	}
	// O LibreOffice 25.2 saiu de desatualizado para fora de suporte (o ciclo acabou em março).
	for estado, n := range map[string]int{StateOutdated: 8, StateEOL: 6, StateOSVendor: 3, StateCurrent: 1, StateBadVersion: 0} {
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
	// PowerShell: a referência é a mais nova da linha instalada (7.6), não a de outra linha.
	if p := por["PowerShell 7.6.6.0-x64"]; p.State != StateCurrent || p.Ref != nil || p.Cycle == nil || p.Cycle.Name != "7.6" || p.Reference != "7.6.6" {
		t.Errorf("PowerShell: %+v", p)
	}
	if l := por["LibreOffice 25.2.1.1"]; l.State != StateEOL || l.Cycle == nil || l.Cycle.Name != "25.2" || l.Reference != "25.2.7" {
		t.Errorf("LibreOffice: %+v", l)
	}
	// Python 3.14: a fonte é o winget; o ciclo só acompanha (suporte até 2030).
	if p := por["Python 3.14.3"]; p.Cycle == nil || p.Cycle.Name != "3.14" || p.EOLSoon {
		t.Errorf("ciclo do Python: %+v", p)
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
	r = EvaluateWith("windows", inventarioDoLab(t), m, Data{Now: agora})
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

// O endoflife.date: o ciclo pelo começo da versão, em pedaços inteiros; o fim do suporte pela
// data (mesmo que o isEol buscado antes ainda diga que não); o aviso de fim próximo.
func TestCiclosDeSuporte(t *testing.T) {
	d := func(s string) *time.Time {
		x, _ := time.Parse("2006-01-02", s)
		return &x
	}
	cs := []Cycle{{Name: "3.1"}, {Name: "3.12"}, {Name: "3"}, {Name: "22"}, {Name: "8.0"}, {Name: "x"}}
	for v, want := range map[string]string{"3.12.7": "3.12", "3.1.2": "3.1", "3.13.0": "3", "22.11.0": "22", "8.0.11": "8.0", "3.12": "3.12", "4.0": "", "abc": ""} {
		got := ""
		if c := CycleFor(v, cs); c != nil {
			got = c.Name
		}
		if got != want {
			t.Errorf("%s: %q, queria %q", v, got, want)
		}
	}
	agora := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	c := Cycle{EOLFrom: d("2026-11-16")}
	if !c.EOLAt(agora) || c.EOLSoon(agora, LifecycleWarning) {
		t.Error("no dia do fim, já sem suporte")
	}
	c = Cycle{EOLFrom: d("2027-01-10")}
	if c.EOLAt(agora) || !c.EOLSoon(agora, LifecycleWarning) || c.EOLSoon(agora, 30*24*time.Hour) {
		t.Error("fim em 55 dias: aviso de 90, não de 30")
	}
	if !(Cycle{IsEOL: true}).EOLAt(agora) || (Cycle{}).EOLAt(agora) || (Cycle{}).EOLSoon(agora, LifecycleWarning) {
		t.Error("sem data")
	}

	// Categoria lifecycle numa regra local: a linha 3.12 na mais nova dela, a 3.9 sem suporte,
	// a 3.11 desatualizada e perto do fim, a 3.99 sem ciclo conhecido.
	m, err := Parse([]byte(`{"version": 1, "apps": [
	  {"id": "py", "os": "windows", "category": "lifecycle", "lifecycle": "python", "match": "^Python (3\\.\\d+\\.\\d+)$", "version_from": "name"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ciclos := []Cycle{
		{Product: "python", Name: "3.12", EOLFrom: d("2028-10-31"), Latest: "3.12.12"},
		{Product: "python", Name: "3.11", EOLFrom: d("2027-01-31"), Latest: "3.11.14"},
		{Product: "python", Name: "3.9", EOLFrom: d("2025-10-31"), Latest: "3.9.25"},
		{Product: "outro", Name: "3.99", Latest: "3.99.1"},
	}
	sw := []protocol.Software{{Name: "Python 3.12.12"}, {Name: "Python 3.11.2"}, {Name: "Python 3.9.25"}, {Name: "Python 3.99.0"}}
	r := EvaluateWith("windows", sw, m, Data{Cycles: ciclos, Now: agora})
	por := map[string]Finding{}
	for _, f := range r.Findings {
		por[f.Software.Name] = f
	}
	if f := por["Python 3.12.12"]; f.State != StateCurrent || f.Reference != "3.12.12" || f.EOLSoon {
		t.Errorf("3.12: %+v", f)
	}
	if f := por["Python 3.11.2"]; f.State != StateOutdated || f.Reference != "3.11.14" || !f.EOLSoon {
		t.Errorf("3.11: %+v", f)
	}
	if f := por["Python 3.9.25"]; f.State != StateEOL || f.Cycle == nil || f.Cycle.Name != "3.9" {
		t.Errorf("3.9: %+v", f)
	}
	if f := por["Python 3.99.0"]; f.State != StateNoReference || f.Cycle != nil {
		t.Errorf("3.99 (o ciclo é de outro produto): %+v", f)
	}
}

// A segunda fonte (o Evergreen) à frente da principal: a referência é a dela, e o achado diz
// que a principal ficou para trás. Atrás ou igual, nada muda.
func TestSegundaFonte(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	sw := []protocol.Software{{Name: "Notepad++ (64-bit x64)", Version: "8.9.8.1", Publisher: "Notepad++ Team"}}
	ref := Ref{App: "notepad-plus-plus", OS: "windows", Version: "8.9.8.1", Source: "winget:Notepad++.Notepad++",
		CheckVersion: "8.9.9", CheckSource: "evergreen:NotepadPlusPlus"}
	f := EvaluateWith("windows", sw, m, Data{Refs: []Ref{ref}, Now: time.Now()}).Findings[0]
	if f.State != StateOutdated || f.Reference != "8.9.9" || !f.Behind {
		t.Errorf("à frente: %+v", f)
	}
	ref.CheckVersion = "8.9.8"
	f = EvaluateWith("windows", sw, m, Data{Refs: []Ref{ref}, Now: time.Now()}).Findings[0]
	if f.State != StateCurrent || f.Reference != "8.9.8.1" || f.Behind {
		t.Errorf("atrás: %+v", f)
	}
}
