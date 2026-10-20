package sbom

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

func infoDeTeste() *debug.BuildInfo {
	return &debug.BuildInfo{
		GoVersion: "go1.27.2",
		Main:      debug.Module{Path: "github.com/tasantiago/patchd", Version: "(devel)"},
		Deps: []*debug.Module{
			{Path: "golang.org/x/text", Version: "v0.43.0", Sum: "h1:abc="},
			{Path: "github.com/jackc/pgx/v5", Version: "v5.7.0", Replace: &debug.Module{Path: "github.com/fork/pgx/v5", Version: "v5.7.1", Sum: "h1:def="}},
			{Path: "example.com/local", Version: "v1.0.0", Replace: &debug.Module{Path: "../local"}},
			{Path: "example.com/velho", Version: "v2.0.0+incompatible"},
			{Path: "example.com/outro", Version: "v1.0.0", Replace: &debug.Module{Path: "/home/quem/deps/outro", Version: "(devel)"}},
		},
		Settings: []debug.BuildSetting{
			{Key: "GOOS", Value: "windows"}, {Key: "GOARCH", Value: "amd64"}, {Key: "-trimpath", Value: "true"},
			{Key: "-ldflags", Value: "-X segredo"}, {Key: "vcs.revision", Value: "8ba97055b6cae67f266bde790a92f33475a01361"},
			{Key: "vcs.time", Value: "2026-10-20T09:00:00Z"}, {Key: "vcs.modified", Value: "false"},
		},
	}
}

func TestBuild(t *testing.T) {
	art := Artifact{Name: "patchd-agent-windows-amd64.exe", Version: "v0.17.11", SHA256: Sum([]byte("exe"))}
	b, err := Build(art, infoDeTeste(), "v0.17.11")
	if err != nil {
		t.Fatal(err)
	}
	if b.BOMFormat != "CycloneDX" || b.SpecVersion != "1.5" || b.Metadata.Timestamp != "2026-10-20T09:00:00Z" {
		t.Errorf("cabeçalho: %+v", b)
	}
	m := b.Metadata.Component
	if m.Name != art.Name || m.Version != "v0.17.11" || m.PURL != "pkg:golang/github.com/tasantiago/patchd@v0.17.11" ||
		len(m.Hashes) != 1 || m.Hashes[0].Content != art.SHA256 {
		t.Errorf("executável: %+v", m)
	}
	data, _ := json.Marshal(b)
	s := string(data)
	for _, quer := range []string{
		`"purl":"pkg:golang/stdlib@1.27.2"`,
		`"purl":"pkg:golang/golang.org/x/text@v0.43.0"`, `{"name":"golang:sum","value":"h1:abc="}`,
		// replace por outro módulo: o que foi compilado é o fork.
		`"purl":"pkg:golang/github.com/fork/pgx/v5@v5.7.1"`, `"value":"github.com/jackc/pgx/v5@v5.7.0"`,
		// replace por pasta: a pasta não vai (é do host do build).
		`"value":"pasta local"`,
		`"purl":"pkg:golang/example.com/velho@v2.0.0%2Bincompatible"`,
		`{"name":"golang:build:GOOS","value":"windows"}`, `"bom-ref":"pkg:golang/github.com/tasantiago/patchd@v0.17.11#patchd-agent-windows-amd64.exe"`,
	} {
		if !strings.Contains(s, quer) {
			t.Errorf("faltou %s", quer)
		}
	}
	for _, nao := range []string{"segredo", "../local", "/home/quem"} {
		if strings.Contains(s, nao) {
			t.Errorf("vazou %q", nao)
		}
	}
	if len(b.Dependencies) != 1 || len(b.Dependencies[0].DependsOn) != len(b.Components) {
		t.Errorf("dependências: %+v", b.Dependencies)
	}

	// Determinístico; outro executável, outro número de série.
	b2, _ := Build(art, infoDeTeste(), "v0.17.11")
	art.SHA256 = Sum([]byte("outro"))
	b3, _ := Build(art, infoDeTeste(), "v0.17.11")
	if b.SerialNumber != b2.SerialNumber || b.SerialNumber == b3.SerialNumber || !strings.HasPrefix(b.SerialNumber, "urn:uuid:") || len(b.SerialNumber) != 45 {
		t.Errorf("número de série: %s %s %s", b.SerialNumber, b2.SerialNumber, b3.SerialNumber)
	}

	// Compilado com alterações não commitadas: recusado.
	bi := infoDeTeste()
	bi.Settings[len(bi.Settings)-1].Value = "true"
	if _, err := Build(art, bi, "x"); !errors.Is(err, ErrModified) {
		t.Errorf("modificado: %v", err)
	}
}

// O executável deste teste é um executável Go de verdade.
func TestBuildDoProprioExecutavel(t *testing.T) {
	self, _ := os.Executable()
	bi, err := buildinfo.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for i := range bi.Settings { // a árvore de quem roda o teste pode estar modificada
		if bi.Settings[i].Key == "vcs.modified" {
			bi.Settings[i].Value = "false"
		}
	}
	b, err := Build(Artifact{Name: "sbom.test"}, bi, "dev")
	if err != nil || b.Components[0].Name != "stdlib" {
		t.Fatalf("%+v %v", b.Components, err)
	}
}

func TestModules(t *testing.T) {
	a, _ := Build(Artifact{Name: "a"}, infoDeTeste(), "x")
	bi := infoDeTeste()
	bi.Deps = bi.Deps[:1]
	c, _ := Build(Artifact{Name: "b"}, bi, "x")
	ms := Modules([]BOM{a, c})
	var text *Module
	for i := range ms {
		if ms[i].Name == "golang.org/x/text" {
			text = &ms[i]
		}
	}
	if text == nil || strings.Join(text.In, ",") != "a,b" || len(ms) != 6 {
		t.Errorf("%+v", ms)
	}
}
