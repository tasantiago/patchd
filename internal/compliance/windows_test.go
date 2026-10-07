package compliance

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Os nomes dos produtos de Windows 11 do catálogo do laboratório (consulta da Aula 6.4,
// parte 1). Os IDs do 23H2 ao 26H1 e o do Windows 10 são os do MSRC; os do 22H2 e do
// " - extra" são ilustrativos. Atenção ao "version" minúsculo do 26H1 x64.
var produtosDoLab = []MSRCProduct{
	{ID: "12086", Name: "Windows 11 Version 22H2 for ARM64-based Systems"},
	{ID: "12085", Name: "Windows 11 Version 22H2 for x64-based Systems"},
	{ID: "12242", Name: "Windows 11 Version 23H2 for ARM64-based Systems"},
	{ID: "12243", Name: "Windows 11 Version 23H2 for x64-based Systems"},
	{ID: "12389", Name: "Windows 11 Version 24H2 for ARM64-based Systems"},
	{ID: "12390", Name: "Windows 11 Version 24H2 for x64-based Systems"},
	{ID: "20437", Name: "Windows 11 Version 25H2 for ARM64-based Systems"},
	{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
	{ID: "20854", Name: "Windows 11 Version 26H1 for ARM64-based Systems"},
	{ID: "20853", Name: "Windows 11 version 26H1 for x64-based Systems"},
	{ID: "20900", Name: "Windows 11 Version 26H1 for x64-based Systems - extra"},
	{ID: "12097", Name: "Windows 10 Version 22H2 for x64-based Systems"},
}

func TestParseWindowsBuild(t *testing.T) {
	for in, want := range map[string]WindowsBuild{
		"26300.9457":       {26300, 9457},
		"10.0.26200.9445":  {26200, 9445},
		" 19045.7725 ":     {19045, 7725},
		"10.0.26200.10001": {26200, 10001},
	} {
		if got, ok := ParseWindowsBuild(in); !ok || got != want {
			t.Errorf("%q: %v %t", in, got, ok)
		}
	}
	for _, in := range []string{"", "26300", "6.3.9600.23398", "26300.9457.1", "abc.1", "26300.-1"} {
		if got, ok := ParseWindowsBuild(in); ok {
			t.Errorf("%q deveria ser recusado: %v", in, got)
		}
	}
}

func TestWindowsProductFor(t *testing.T) {
	win := func(version, build, arch, edition string) protocol.OSInfo {
		return protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: version, Build: build, Arch: arch, Edition: edition}
	}
	casos := []struct {
		nome   string
		info   protocol.OSInfo
		como   string
		ids    []string
		trecho string // no motivo, quando não há produto
	}{
		{nome: "25H2 x64", info: win("25H2", "26200.9445", "amd64", "Professional"), ids: []string{"20438"}},
		{nome: "26H1 com version minúsculo, sem o - extra", info: win("26H1", "28000.2954", "amd64", "Professional"), ids: []string{"20853"}},
		{nome: "ARM64", info: win("24H2", "26100.9445", "arm64", "Professional"), ids: []string{"12389"}},
		{nome: "família pelo build, não pelo nome", info: protocol.OSInfo{Name: "Windows 10 Pro", Version: "22H2", Build: "19045.7725", Arch: "amd64"}, ids: []string{"12097"}},
		{nome: "o 26H2 do laboratório", info: win("26H2", "26300.9457", "amd64", "Professional"),
			trecho: "Windows 11 26H2 (amd64) não está no catálogo do MSRC; versões no catálogo: 22H2, 23H2, 24H2, 25H2, 26H1. Se for uma versão nova"},
		{nome: "-como", info: win("26H2", "26300.9457", "amd64", "Professional"), como: "25h2", ids: []string{"20438"}},
		{nome: "-como inexistente", info: win("26H2", "26300.9457", "amd64", "Professional"), como: "27H2", trecho: "Windows 11 27H2 (amd64) não está no catálogo"},
		{nome: "Server", info: win("24H2", "26100.9445", "amd64", "ServerStandard"), trecho: "Windows Server (ServerStandard) ainda não é avaliado"},
		{nome: "sem UBR", info: win("25H2", "26200", "amd64", ""), trecho: `build "26200" ilegível`},
		{nome: "sem DisplayVersion", info: win("", "26200.9445", "amd64", ""), trecho: "não informa a versão"},
		{nome: "arquitetura", info: win("25H2", "26200.9445", "riscv64", ""), trecho: `arquitetura "riscv64"`},
	}
	for _, c := range casos {
		got, motivo, ok := WindowsProductFor(c.info, c.como, produtosDoLab)
		if c.ids != nil {
			if !ok || !slices.Equal(got.IDs, c.ids) || got.Assumed != (c.como != "") {
				t.Errorf("%s: %+v %q %t", c.nome, got, motivo, ok)
			}
			continue
		}
		if ok || !strings.Contains(motivo, c.trecho) {
			t.Errorf("%s: %+v %q", c.nome, got, motivo)
		}
	}
}

// Linhas no formato do catálogo para o 25H2 x64. Os KBs e os builds .9445 (setembro),
// .9457 (KB5129195) e .9448 (hotpatch) são os do laboratório; os demais são ilustrativos.
func linhasDoProduto() []WindowsFix {
	ago := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	fix := func(doc string, quando time.Time, cve, sev string, score float64, kb, sub, build string) WindowsFix {
		return WindowsFix{Document: doc, Released: quando, CVE: cve, Title: "título " + cve + " " + doc,
			Severity: sev, Score: score, KB: kb, SubType: sub, Build: build}
	}
	l := []WindowsFix{
		// Setembro: corrigida pelo KB de setembro.
		fix("2026-Sep", set, "CVE-2026-50349", "Important", 7.8, "5124008", "Security Update", "10.0.26200.9445"),
		// Agosto, relançada: o KB de agosto e o KB5129195, mais novo que o de setembro.
		fix("2026-Aug", ago, "CVE-2026-40001", "Important", 7.0, "5121003", "Security Update", "10.0.26200.9350"),
		fix("2026-Aug", ago, "CVE-2026-40001", "Important", 7.0, "5129195", "Security Update", "10.0.26200.9457"),
		// Explorada (MSRC), corrigida em agosto; e a hotpatch do mesmo mês, com as duas grafias.
		fix("2026-Aug", ago, "CVE-2026-40002", "Important", 7.8, "5121003", "Security Update", "10.0.26200.9350"),
		fix("2026-Aug", ago, "CVE-2026-40002", "Important", 7.8, "5129241", "Security Hotpatch Update", "10.0.26200.9448"),
		// Só hotpatch: sem atualização cumulativa no catálogo.
		fix("2026-Aug", ago, "CVE-2026-40003", "Moderate", 5.5, "5129241", "SecurityHotpatchUpdate", "10.0.26200.9448"),
		// "Security Only" num produto que não tem esse modelo: desconsiderada.
		fix("2026-Aug", ago, "CVE-2026-40004", "Critical", 9.8, "5074204", "Security Only", "10.0.26200.9300"),
		fix("2026-Aug", ago, "CVE-2026-40004", "Critical", 9.8, "5121003", "Security Update", "10.0.26200.9350"),
		// Afetada, sem KB para o produto.
		fix("2026-Sep", set, "CVE-2026-50400", "Low", 0, "", "", ""),
		// Revisada em setembro: severidade maior e título novo; e build ilegível.
		fix("2026-Aug", ago, "CVE-2026-40005", "Moderate", 5.0, "5121003", "Security Update", "10.0.26200.9350"),
		fix("2026-Sep", set, "CVE-2026-40005", "Important", 7.5, "5124008", "Security Update", "release notes"),
		// No KEV, corrigida em setembro.
		fix("2026-Sep", set, "CVE-2026-50500", "Important", 7.8, "5124008", "Security Update", "10.0.26200.9445"),
		// Build de outra linha no mesmo produto (inconsistência do MSRC): conta pelo UBR.
		fix("2026-Sep", set, "CVE-2026-50600", "Important", 7.0, "5124008", "Security Update", "10.0.26100.9445"),
	}
	l[3].Exploited, l[4].Exploited = true, true
	l[11].KEV = true
	return l
}

func TestEvaluateWindows(t *testing.T) {
	cves := func(l []WindowsCVE) []string {
		var out []string
		for _, c := range l {
			out = append(out, c.CVE)
		}
		return out
	}

	// Em agosto (.9350): só as de setembro estão pendentes.
	r := EvaluateWindows(WindowsBuild{26200, 9350}, linhasDoProduto())
	if r.CVEs != 9 || r.Foreign || r.Hotpatch != 2 || r.Ignored["Security Only"] != 1 || r.Ignored["build ilegível"] != 1 {
		t.Fatalf("contagens: %+v", r)
	}
	if !slices.Equal(r.Majors, []int{26100, 26200}) {
		t.Errorf("builds do produto: %v", r.Majors)
	}
	// KEV primeiro; depois severidade, CVSS e ID.
	if got := cves(r.Pending); !slices.Equal(got, []string{"CVE-2026-50500", "CVE-2026-50349", "CVE-2026-50600"}) {
		t.Errorf("pendentes: %v", got)
	}
	if r.Target != (WindowsBuild{26200, 9445}) || r.TargetKB != "5124008" {
		t.Errorf("alvo: %v KB%s", r.Target, r.TargetKB)
	}
	// O relançamento (.9457) está acima, mas a menor correção (.9350) já está instalada.
	if got := cves(r.Rereleased); !slices.Equal(got, []string{"CVE-2026-40001"}) {
		t.Errorf("relançadas: %v", got)
	}
	if got := cves(r.NoFix); !slices.Equal(got, []string{"CVE-2026-40003", "CVE-2026-50400"}) {
		t.Errorf("sem correção: %v", got)
	}

	// Antes de agosto (.9300): tudo com correção está pendente; o alvo é o relançamento.
	r = EvaluateWindows(WindowsBuild{26200, 9300}, linhasDoProduto())
	if len(r.Pending) != 7 || r.Target.UBR != 9457 || r.TargetKB != "5129195" {
		t.Fatalf("antes de agosto: %d pendentes, alvo %v KB%s", len(r.Pending), r.Target, r.TargetKB)
	}
	// Explorada pelo MSRC vem logo depois do KEV, mesmo com severidade menor que a Critical.
	if got := cves(r.Pending)[:3]; !slices.Equal(got, []string{"CVE-2026-50500", "CVE-2026-40002", "CVE-2026-40004"}) {
		t.Errorf("ordem: %v", got)
	}
	for _, c := range r.Pending {
		if c.CVE == "CVE-2026-40005" && (c.Severity != "Important" || c.Score != 7.5 || c.Document != "2026-Aug" || c.Title != "título CVE-2026-40005 2026-Sep") {
			t.Errorf("revisada: %+v", c)
		}
		if c.CVE == "CVE-2026-40001" && (c.Min.UBR != 9350 || c.MinKB != "5121003" || c.Max.UBR != 9457 || c.MaxKB != "5129195") {
			t.Errorf("relançada: %+v", c)
		}
	}

	// O 26H2 do laboratório: build de fora do produto; o UBR é o do KB5129195.
	r = EvaluateWindows(WindowsBuild{26300, 9457}, linhasDoProduto())
	if !r.Foreign || r.Evidence != "KB5129195" || len(r.Pending) != 0 || len(r.Rereleased) != 0 {
		t.Errorf("26H2: %+v", r)
	}
	// Build de fora sem UBR correspondente: sem evidência.
	if r = EvaluateWindows(WindowsBuild{22631, 7582}, linhasDoProduto()); !r.Foreign || r.Evidence != "" {
		t.Errorf("23H2 contra o 25H2: %+v", r)
	}
	// Linha compartilhada: o 24H2 (26100) não é de fora do produto 25H2.
	if r = EvaluateWindows(WindowsBuild{26100, 9445}, linhasDoProduto()); r.Foreign || len(r.Pending) != 0 {
		t.Errorf("24H2 na linha do 25H2: %+v", r)
	}
}

func TestNormalizeSubType(t *testing.T) {
	for _, s := range []string{"Security Hotpatch Update", "SecurityHotpatchUpdate", " security-hotpatch_update "} {
		if got := normalizeSubType(s); got != "securityhotpatchupdate" {
			t.Errorf("%q: %q", s, got)
		}
	}
}
