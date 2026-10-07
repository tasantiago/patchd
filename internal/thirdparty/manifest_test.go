package thirdparty

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.For("windows")) < 15 || len(m.For("macos")) < 3 {
		t.Errorf("regras: %d windows, %d macos", len(m.For("windows")), len(m.For("macos")))
	}
}

func TestParseRecusa(t *testing.T) {
	casos := map[string]string{
		`{"version": 2, "apps": []}`: "versão 2",
		`{"version": 1, "apps": [{"id": "x", "os": "linux", "category": "eol", "match": "x"}]}`:                                                                  "use windows ou macos",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "feed", "match": "x"}]}`:                                                               "exige source",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "feed", "match": "x", "source": {"type": "ftp", "id": "a"}}]}`:                         "fonte \"ftp\"",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "minimum", "match": "x", "min_version": "Auto"}]}`:                                     "min_version numérica",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "eol", "match": "(x"}]}`:                                                               "match:",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "eol", "match": "x", "version_from": "name"}]}`:                                        "exige um grupo",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "talvez", "match": "x"}]}`:                                                             "categoria \"talvez\"",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "eol", "match": "x", "publiser": "y"}]}`:                                               "unknown field",
		`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "eol", "match": "x"}, {"id": "x", "os": "windows", "category": "eol", "match": "y"}]}`: "repetida",
	}
	for entrada, trecho := range casos {
		if _, err := Parse([]byte(entrada)); err == nil || !strings.Contains(err.Error(), trecho) {
			t.Errorf("%s: %v (queria %q)", entrada, err, trecho)
		}
	}
}

func TestLoadComManifestoLocal(t *testing.T) {
	local := `{"version": 1, "apps": [
	  {"id": "google-chrome", "os": "windows", "name": "Chrome (política)", "category": "minimum", "match": "^Google Chrome$", "min_version": "150"},
	  {"id": "java-8", "os": "windows", "name": "Java 8", "category": "minimum", "publisher": "^Oracle", "match": "^Java 8 Update", "min_version": "8.0.5030"}]}`
	path := filepath.Join(t.TempDir(), "local.json")
	if err := os.WriteFile(path, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	padrao, _ := Default()
	if len(m.Apps) != len(padrao.Apps)+1 {
		t.Errorf("regras: %d (padrão %d)", len(m.Apps), len(padrao.Apps))
	}
	// A regra local substitui a do padrão no mesmo lugar; a nova vai para o fim.
	win := m.For("windows")
	if win[0].Key() != "google-chrome/windows" || win[0].Category != CategoryMinimum || win[len(win)-1].ID != "java-8" {
		t.Errorf("ordem depois da fusão: %s ... %s", win[0].Key(), win[len(win)-1].Key())
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nao-existe.json")); err == nil {
		t.Error("manifesto local inexistente deveria falhar")
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"154.0.8037.93": "154.0.8037.93", "26.02.00.0": "26.02.00.0", "140.17.0esr": "140.17.0",
		"1.2.99.317.g9bd8c54d": "1.2.99.317", " 8.9.7 ": "8.9.7", "26": "26",
	} {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("%q: %q %t", in, got, ok)
		}
	}
	for _, in := range []string{"", "Auto Updatable", "2022-12-02 15:42:16.000-04:00", "3.14-64", "v1.2"} {
		if got, ok := Normalize(in); ok {
			t.Errorf("%q deveria ser recusada: %q", in, got)
		}
	}
}
