package apple

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

func TestAppleRootPool(t *testing.T) {
	pool, err := AppleRootPool()
	if err != nil || pool == nil {
		t.Fatalf("pool: %v", err)
	}
	// Arquivo trocado: o pool não é montado.
	orig := appleRootPEM
	t.Cleanup(func() { appleRootPEM = orig })
	appleRootPEM = []byte(strings.Replace(string(orig), "MIIEuzCC", "MIIEuzCD", 1))
	if _, err := AppleRootPool(); err == nil {
		t.Error("certificado alterado deveria ser recusado")
	}
	appleRootPEM = append(append([]byte{}, orig...), orig...)
	if _, err := AppleRootPool(); err == nil || !strings.Contains(err.Error(), "mais de um") {
		t.Errorf("dois certificados: %v", err)
	}
}

func TestParseGDMF(t *testing.T) {
	vs, err := ParseGDMF(abrir(t, "gdmf-pmv-recorte.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 8 versões públicas (do 11.7.11 ao 27.0.1, com dois builds do 27.0.1) e 2 melhorias.
	if len(vs) != 10 {
		t.Fatalf("versões: %d %+v", len(vs), vs)
	}
	var builds27 []string
	extras := 0
	for _, v := range vs {
		if v.ProductVersion == "27.0.1" {
			builds27 = append(builds27, v.Build)
		}
		if v.Extra == "(a)" {
			extras++
			if v.PrerequisiteBuild == "" {
				t.Errorf("melhoria sem build pré-requisito: %+v", v)
			}
		}
	}
	if len(builds27) != 2 || extras != 2 {
		t.Errorf("27.0.1: %v; melhorias: %d", builds27, extras)
	}
	if !vs[0].Posting.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) || vs[0].Devices != 3 {
		t.Errorf("primeira: %+v", vs[0])
	}

	for _, ruim := range []string{`não é json`, `{"PublicAssetSets": {"macOS": [{"ProductVersion": "26.7.1"}]}}`,
		`{"PublicAssetSets": {"macOS": [{"ProductVersion": "26.7.1", "Build": "X", "PostingDate": "28/09/2026"}]}}`} {
		if _, err := ParseGDMF(strings.NewReader(ruim)); err == nil {
			t.Errorf("%s deveria ser recusado", ruim)
		}
	}
}

func TestParseSOFA(t *testing.T) {
	f, err := ParseSOFA(abrir(t, "sofa-macos-v2-recorte.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.UpdateHash == "" || len(f.Releases) != 7 {
		t.Fatalf("feed: hash %q, %d versões", f.UpdateHash, len(f.Releases))
	}
	por := map[string]Release{}
	for _, r := range f.Releases {
		por[r.ProductVersion] = r
	}
	t261 := por["26.7.1"]
	if t261.Major != "Tahoe 26" || t261.MajorNumber != "26" || len(t261.Builds) != 1 || t261.Builds[0] != "25G241" ||
		!t261.Date.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) || !strings.Contains(t261.SecurityInfo, "support.apple.com") {
		t.Errorf("26.7.1: %+v", t261)
	}
	if len(t261.CVEs) != 1 || t261.CVEs[0].ID != "CVE-2026-86950" || !t261.CVEs[0].Exploited || !t261.CVEs[0].InKEV {
		t.Errorf("CVE explorada do 26.7.1: %+v", t261.CVEs)
	}
	// A 27.0 corrige 210 CVEs; o recorte guarda 4, em ordem, com a explorada marcada.
	g := por["27.0"]
	if g.UniqueCVEs != 210 || len(g.CVEs) != 4 || g.CVEs[0].ID > g.CVEs[1].ID {
		t.Errorf("27.0: %d CVEs no feed, %+v", g.UniqueCVEs, g.CVEs)
	}
	// A major antiga (Monterey) também entra: o fim de suporte é decisão da 6.4.
	if m := por["12.7.6"]; len(m.Builds) != 1 || m.MajorNumber != "12" {
		t.Errorf("12.7.6: %+v", m)
	}
	// Versão antiga sem build no SOFA (114 no feed real): lista vazia, não [""].
	sem, err := ParseSOFA(strings.NewReader(`{"OSVersions": [{"OSVersion": "Sonoma 14", "SecurityReleases": [
	  {"ProductVersion": "14.8.5", "Build": "", "AllBuilds": [], "ReleaseDate": "2026-03-24T00:00:00Z", "CVEs": {}}]}]}`))
	if err != nil || len(sem.Releases[0].Builds) != 0 {
		t.Errorf("sem build: %+v %v", sem.Releases, err)
	}

	for _, ruim := range []string{
		`{"OSVersions": [{"OSVersion": "Sem Número", "SecurityReleases": []}]}`,
		`{"OSVersions": [{"OSVersion": "Tahoe 26", "SecurityReleases": [{"ProductVersion": "15.8.1", "ReleaseDate": "2026-09-28T00:00:00Z"}]}]}`,
		`{"OSVersions": [{"OSVersion": "Tahoe 26", "SecurityReleases": [{"ProductVersion": "26.1", "ReleaseDate": "ontem"}]}]}`,
	} {
		if _, err := ParseSOFA(strings.NewReader(ruim)); err == nil {
			t.Errorf("%s deveria ser recusado", ruim)
		}
	}

	// Explorada citada fora do mapa de CVEs entra assim mesmo.
	f, err = ParseSOFA(strings.NewReader(`{"UpdateHash": "x", "OSVersions": [{"OSVersion": "Tahoe 26", "SecurityReleases": [
	  {"ProductVersion": "26.1", "Build": "25B1", "ReleaseDate": "2026-01-01T00:00:00Z", "ActivelyExploitedCVEs": ["CVE-2026-1"], "CVEs": {}}]}]}`))
	if err != nil || len(f.Releases[0].CVEs) != 1 || !f.Releases[0].CVEs[0].Exploited {
		t.Errorf("explorada fora do mapa: %+v %v", f.Releases, err)
	}
}

// O hash do gdmf depende do conteúdo, não da forma da resposta.
func TestGDMFHash(t *testing.T) {
	a := `{"PublicAssetSets": {"macOS": [
	  {"ProductVersion": "26.7.1", "Build": "25G241", "PostingDate": "2026-09-28", "SupportedDevices": ["J1", "J2"]},
	  {"ProductVersion": "15.8.1", "Build": "24H32", "PostingDate": "2026-09-28", "SupportedDevices": ["J1"]}]}}`
	// A mesma coisa em outra ordem, com outra formatação e modelos listados em outra ordem.
	b := `{"PublicAssetSets":{"macOS":[{"SupportedDevices":["J1"],"Build":"24H32","ProductVersion":"15.8.1","PostingDate":"2026-09-28"},
	  {"ProductVersion":"26.7.1","Build":"25G241","PostingDate":"2026-09-28","SupportedDevices":["J2","J1"]}]},"iOS":[]}`
	// Um modelo a mais no 26.7.1: o conteúdo mudou.
	c := strings.Replace(a, `["J1", "J2"]`, `["J1", "J2", "J3"]`, 1)

	hash := func(s string) string {
		vs, err := ParseGDMF(strings.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		return GDMFHash(vs)
	}
	if hash(a) != hash(b) {
		t.Error("mesmo conteúdo, hash diferente")
	}
	if hash(a) == hash(c) || len(hash(a)) != 64 {
		t.Error("conteúdo diferente, mesmo hash")
	}
}
