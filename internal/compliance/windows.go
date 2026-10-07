package compliance

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// MSRCProduct é um produto do catálogo do MSRC: o ID é estável entre os documentos.
type MSRCProduct struct {
	ID   string
	Name string
}

// WindowsFix é uma linha do catálogo do MSRC para o produto da máquina: uma CVE de um
// documento e, quando o documento traz, uma correção (KB e build) para o produto.
type WindowsFix struct {
	Document  string    // 2026-Sep
	Released  time.Time // publicação inicial do documento
	CVE       string
	Title     string
	Severity  string  // severidade no produto: Critical, Important, Moderate, Low
	Score     float64 // CVSS base no produto; 0 quando não há
	Exploited bool    // o MSRC diz "Exploited:Yes"
	KEV       bool    // a CVE está no KEV da CISA
	KB        string  // vazio: o documento não traz correção com KB para o produto
	SubType   string  // como veio: Security Update, Security Hotpatch Update, SecurityHotpatchUpdate...
	Build     string  // fixed_build: 10.0.26200.9445
}

// WindowsBuild é o build de uma máquina ou de uma correção: o número do build (26200) e o
// UBR, a revisão que cada atualização cumulativa aumenta (9445).
type WindowsBuild struct {
	Major int
	UBR   int
}

func (b WindowsBuild) String() string { return fmt.Sprintf("%d.%d", b.Major, b.UBR) }

// ParseWindowsBuild lê "26300.9457" (inventário) ou "10.0.26300.9457" (MSRC).
func ParseWindowsBuild(s string) (WindowsBuild, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	switch {
	case len(parts) == 4 && parts[0] == "10" && parts[1] == "0":
		parts = parts[2:]
	case len(parts) != 2:
		return WindowsBuild{}, false
	}
	major, err1 := strconv.Atoi(parts[0])
	ubr, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || major <= 0 || ubr < 0 {
		return WindowsBuild{}, false
	}
	return WindowsBuild{Major: major, UBR: ubr}, true
}

// Primeiro build do Windows 11 (o mesmo limite do agente, em internal/inventory).
const firstWindows11Build = 22000

// windowsSharedServicing são builds que recebem as mesmas atualizações cumulativas, com o
// mesmo UBR: o MSRC lista o mesmo KB, com o mesmo UBR, nos dois produtos (KB5124008 é
// 10.0.26100.9445 no 24H2 e 10.0.26200.9445 no 25H2). Dentro de uma linha, só o UBR conta.
var windowsSharedServicing = [][]int{
	{22621, 22631}, // Windows 11 22H2 e 23H2
	{26100, 26200}, // Windows 11 24H2 e 25H2
}

// archLabel é como o MSRC escreve a arquitetura no nome do produto.
var archLabel = map[string]string{
	"amd64": "x64-based Systems",
	"arm64": "ARM64-based Systems",
	"386":   "32-bit Systems",
}

// WindowsTarget é o produto do MSRC escolhido para uma máquina.
type WindowsTarget struct {
	Name    string   // Windows 11 Version 25H2 for x64-based Systems
	IDs     []string // IDs do MSRC com esse nome (normalmente um)
	Version string   // a versão usada no nome (a do inventário, ou a de -como)
	Assumed bool     // a versão veio de -como, não do inventário
}

// WindowsProductFor escolhe o produto do MSRC de uma máquina Windows. A família (10 ou 11)
// vem do número do build, não do nome; a versão vem do DisplayVersion do inventário, ou de
// assume (-como). O nome tem de ser igual ao do MSRC, sem diferença de maiúsculas ("version"
// aparece minúsculo em alguns produtos) e sem sufixos como " - extra".
// Devolve ok=false e o motivo quando não há produto.
func WindowsProductFor(info protocol.OSInfo, assume string, products []MSRCProduct) (WindowsTarget, string, bool) {
	if strings.HasPrefix(info.Edition, "Server") {
		return WindowsTarget{}, fmt.Sprintf("Windows Server (%s) ainda não é avaliado: os produtos do MSRC são outros (Windows Server 2022, 2025...)", info.Edition), false
	}
	b, ok := ParseWindowsBuild(info.Build)
	if !ok {
		return WindowsTarget{}, fmt.Sprintf("build %q ilegível no inventário (esperado build.UBR, ex.: 26200.9457)", info.Build), false
	}
	family := "Windows 10"
	if b.Major >= firstWindows11Build {
		family = "Windows 11"
	}
	arch, ok := archLabel[info.Arch]
	if !ok {
		return WindowsTarget{}, fmt.Sprintf("arquitetura %q sem produto conhecido no MSRC", info.Arch), false
	}
	t := WindowsTarget{Version: info.Version}
	if assume != "" {
		t.Version, t.Assumed = strings.ToUpper(strings.TrimSpace(assume)), true
	}
	if t.Version == "" {
		return WindowsTarget{}, "o inventário não informa a versão (DisplayVersion)", false
	}
	t.Name = fmt.Sprintf("%s Version %s for %s", family, t.Version, arch)

	prefix, suffix := strings.ToLower(family+" Version "), strings.ToLower(" for "+arch)
	var versions []string
	for _, p := range products {
		n := strings.ToLower(p.Name)
		if n == strings.ToLower(t.Name) {
			t.IDs = append(t.IDs, p.ID)
		}
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			versions = append(versions, strings.ToUpper(n[len(prefix):len(n)-len(suffix)]))
		}
	}
	if len(t.IDs) > 0 {
		return t, "", true
	}
	slices.Sort(versions)
	versions = slices.Compact(versions)
	note := fmt.Sprintf("%s %s (%s) não está no catálogo do MSRC", family, t.Version, info.Arch)
	if len(versions) > 0 {
		note += fmt.Sprintf("; versões no catálogo: %s", strings.Join(versions, ", "))
	} else {
		note += "; o catálogo do MSRC não tem produtos dessa família (sincronizado?)"
	}
	if !t.Assumed {
		note += ". Se for uma versão nova da mesma linha de atualizações, avalie com -como VERSÃO"
	}
	return WindowsTarget{}, note, false
}

// WindowsCVE é uma CVE do produto, com as correções do catálogo reunidas de todos os
// documentos em que aparece.
type WindowsCVE struct {
	CVE       string
	Title     string
	Severity  string
	Score     float64
	Exploited bool // MSRC
	KEV       bool
	Document  string    // primeiro documento em que aparece
	Released  time.Time // publicação desse documento
	Min, Max  WindowsBuild
	MinKB     string
	MaxKB     string
}

// Explored diz se a CVE é explorada: pelo MSRC ou pelo KEV.
func (c WindowsCVE) Explored() bool { return c.Exploited || c.KEV }

// WindowsResult é a avaliação de uma máquina Windows contra o catálogo do MSRC.
type WindowsResult struct {
	Build      WindowsBuild
	Majors     []int        // builds das atualizações cumulativas do produto (com a linha compartilhada)
	Foreign    bool         // o build da máquina não é de nenhum deles
	Evidence   string       // KB cujo build tem exatamente o UBR da máquina, quando há
	CVEs       int          // CVEs do produto no catálogo
	Pending    []WindowsCVE // build abaixo da menor correção
	Rereleased []WindowsCVE // corrigida, mas a CVE ganhou correção mais nova acima do build
	NoFix      []WindowsCVE // sem atualização cumulativa no catálogo para o produto
	Hotpatch   int          // correções de hotpatch desconsideradas
	Ignored    map[string]int
	Target     WindowsBuild // a maior correção entre as CVEs pendentes
	TargetKB   string
}

// normalizeSubType junta as grafias do MSRC: "Security Hotpatch Update" e
// "SecurityHotpatchUpdate" viram "securityhotpatchupdate".
func normalizeSubType(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '_' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
}

// severityRank junta as escalas do MSRC (Important, Moderate) e do SOFA (High, Medium).
var severityRank = map[string]int{"critical": 4, "important": 3, "high": 3, "moderate": 2, "medium": 2, "low": 1}

// EvaluateWindows compara o build da máquina com as correções do produto.
//
// Regras (Aula 6.4, parte 3):
//   - a unidade é a CVE: o alvo nunca é "o maior build do mês", porque o MSRC revisa
//     documentos antigos e acrescenta KBs de meses seguintes;
//   - só contam as atualizações cumulativas ("Security Update"). Hotpatch fica de fora,
//     porque o inventário não diz se a máquina está inscrita; outros subtipos ("Security
//     Only" num produto que não tem esse modelo) também, e são contados;
//   - dentro do produto (e da linha compartilhada), só o UBR é comparado;
//   - a CVE está pendente se o UBR da máquina é menor que o da menor correção. Se ficou
//     entre a menor e a maior (a correção foi relançada), está corrigida, com aviso;
//   - o alvo é a maior correção entre as CVEs pendentes: as atualizações são cumulativas.
func EvaluateWindows(machine WindowsBuild, rows []WindowsFix) WindowsResult {
	res := WindowsResult{Build: machine, Ignored: map[string]int{}}
	byCVE := map[string]*WindowsCVE{}
	latest := map[string]time.Time{}
	has := map[string]bool{}
	majors := map[int]bool{}

	for _, r := range rows {
		c, ok := byCVE[r.CVE]
		if !ok {
			c = &WindowsCVE{CVE: r.CVE, Document: r.Document, Released: r.Released}
			byCVE[r.CVE] = c
		}
		if r.Released.Before(c.Released) {
			c.Document, c.Released = r.Document, r.Released
		}
		if !r.Released.Before(latest[r.CVE]) {
			latest[r.CVE] = r.Released
			if r.Title != "" {
				c.Title = r.Title
			}
		}
		if severityRank[strings.ToLower(r.Severity)] > severityRank[strings.ToLower(c.Severity)] {
			c.Severity = r.Severity
		}
		c.Score = max(c.Score, r.Score)
		c.Exploited = c.Exploited || r.Exploited
		c.KEV = c.KEV || r.KEV

		if r.KB == "" {
			continue
		}
		st := normalizeSubType(r.SubType)
		switch {
		case strings.Contains(st, "hotpatch"):
			res.Hotpatch++
			continue
		case st != "securityupdate":
			res.Ignored[r.SubType]++
			continue
		}
		b, ok := ParseWindowsBuild(r.Build)
		if !ok {
			res.Ignored["build ilegível"]++
			continue
		}
		majors[b.Major] = true
		if b.UBR == machine.UBR && res.Evidence == "" {
			res.Evidence = "KB" + r.KB
		}
		if !has[r.CVE] || b.UBR < c.Min.UBR || (b.UBR == c.Min.UBR && prefer(b, c.Min, machine)) {
			c.Min, c.MinKB = b, r.KB
		}
		if !has[r.CVE] || b.UBR > c.Max.UBR || (b.UBR == c.Max.UBR && prefer(b, c.Max, machine)) {
			c.Max, c.MaxKB = b, r.KB
		}
		has[r.CVE] = true
	}

	for _, line := range windowsSharedServicing {
		if slices.ContainsFunc(line, func(m int) bool { return majors[m] }) {
			for _, m := range line {
				majors[m] = true
			}
		}
	}
	for m := range majors {
		res.Majors = append(res.Majors, m)
	}
	slices.Sort(res.Majors)
	res.Foreign = len(res.Majors) > 0 && !majors[machine.Major]
	if !res.Foreign {
		res.Evidence = ""
	}

	res.CVEs = len(byCVE)
	// Ordem fixa: o alvo não pode depender da ordem de um map (no empate de UBR, o build
	// escolhido mudava de uma execução para outra).
	ids := make([]string, 0, len(byCVE))
	for id := range byCVE {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := byCVE[id]
		switch {
		case !has[c.CVE]:
			res.NoFix = append(res.NoFix, *c)
		case machine.UBR < c.Min.UBR:
			res.Pending = append(res.Pending, *c)
			if c.Max.UBR > res.Target.UBR || (c.Max.UBR == res.Target.UBR && prefer(c.Max, res.Target, machine)) {
				res.Target, res.TargetKB = c.Max, c.MaxKB
			}
		case machine.UBR < c.Max.UBR:
			res.Rereleased = append(res.Rereleased, *c)
		}
	}
	// KEV primeiro, depois explorada pelo MSRC; depois severidade, CVSS e ID.
	exploitRank := func(c WindowsCVE) int { return 2*boolInt(c.KEV) + boolInt(c.Exploited && !c.KEV) }
	order := func(a, b WindowsCVE) int {
		return cmp.Or(
			-cmp.Compare(exploitRank(a), exploitRank(b)),
			-cmp.Compare(severityRank[strings.ToLower(a.Severity)], severityRank[strings.ToLower(b.Severity)]),
			-cmp.Compare(a.Score, b.Score),
			strings.Compare(a.CVE, b.CVE))
	}
	slices.SortFunc(res.Pending, order)
	slices.SortFunc(res.Rereleased, order)
	slices.SortFunc(res.NoFix, order)
	return res
}

// prefer decide o empate entre dois builds com o mesmo UBR (o mesmo KB em 26100 e 26200):
// vale o da linha da máquina; fora dela, o de número maior. Assim o resultado não depende da
// ordem em que as linhas chegam.
func prefer(a, b, machine WindowsBuild) bool {
	if (a.Major == machine.Major) != (b.Major == machine.Major) {
		return a.Major == machine.Major
	}
	return a.Major > b.Major
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
