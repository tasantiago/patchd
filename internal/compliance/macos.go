package compliance

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/version"
)

// MacOSMajor resume uma major do macOS no catálogo (SOFA).
type MacOSMajor struct {
	Number     int
	Name       string    // como o SOFA chama: "Tahoe 26"
	Launched   time.Time // data do x.0 (ou da versão mais antiga do catálogo, se o x.0 faltar)
	Latest     string    // versão de segurança mais recente da major
	LatestDate time.Time
}

// MacOSFix é uma CVE corrigida por uma versão de segurança da major da máquina.
type MacOSFix struct {
	Version   string // 26.7.1
	Released  time.Time
	CVE       string
	Exploited bool // exploração ativa, segundo o boletim da Apple
	KEV       bool
	Severity  string // a do SOFA; pode vir vazia
}

// MacModel é um modelo de Mac e as majors que ele aceita (seção Models do SOFA).
type MacModel struct {
	ID     string // identificador do modelo: Mac14,14
	Name   string // Mac Studio (M2 Ultra, 2023)
	Majors []int  // da mais nova para a mais antiga
}

// MaxMajor é a major mais nova que o modelo aceita (0 se o SOFA não informa nenhuma).
func (m MacModel) MaxMajor() int {
	if len(m.Majors) == 0 {
		return 0
	}
	return slices.Max(m.Majors)
}

// MacOSCVE é uma CVE da avaliação do macOS.
type MacOSCVE struct {
	CVE       string
	Exploited bool
	KEV       bool
	Severity  string
	Min, Max  string    // na major da máquina: a menor e a maior versão que corrigem
	Released  time.Time // data da menor correção
	Elsewhere []string  // sem correção na major: as versões de outras majors que corrigem
}

// SupportState diz se uma major do macOS ainda recebe atualizações de segurança.
type SupportState int

const (
	SupportUnknown     SupportState = iota // sem dado para decidir
	Supported                              // recebeu versão de segurança no ciclo da major mais nova
	SupportUnconfirmed                     // major nova recém-lançada; esta ainda não recebeu, mas costuma receber
	SupportEnded                           // não recebe mais
)

// macOSSupportGrace é o prazo, depois do lançamento de uma major nova, em que as duas
// anteriores ainda podem receber a versão de segurança do ciclo sem serem dadas como
// encerradas.
const macOSSupportGrace = 30 * 24 * time.Hour

// MacOSSupport é a situação de suporte de uma major.
type MacOSSupport struct {
	State  SupportState
	Major  MacOSMajor // a major pedida (Number preenchido mesmo fora do catálogo)
	Newest MacOSMajor // a major mais nova do catálogo
	Note   string
}

// MacOSSupportOf decide o suporte de uma major pelo próprio catálogo. A Apple não publica
// uma política; o que o dado mostra é que, quando sai uma major nova (27.0, em 14/09/2026),
// as majors que continuam atendidas recebem uma versão de segurança no mesmo ciclo (26.7 e
// 15.8, no mesmo dia), e a que ficou de fora (Sonoma 14, última em 06/08) não recebe mais.
//
// Regra: a major mais nova tem suporte; as outras têm suporte se a última versão de
// segurança delas é do dia do lançamento da mais nova ou depois. Nos 30 dias seguintes ao
// lançamento, as duas majors anteriores (pela posição no catálogo: 26 e 15, quando a mais
// nova é a 27) sem essa versão ficam "a confirmar", não encerradas.
func MacOSSupportOf(majors []MacOSMajor, major int, now time.Time) MacOSSupport {
	s := MacOSSupport{Major: MacOSMajor{Number: major}}
	if len(majors) == 0 {
		s.Note = "o catálogo do SOFA está vazio (sincronize com catalog sync -source apple)"
		return s
	}
	s.Newest = slices.MaxFunc(majors, func(a, b MacOSMajor) int { return cmp.Compare(a.Number, b.Number) })
	oldest := slices.MinFunc(majors, func(a, b MacOSMajor) int { return cmp.Compare(a.Number, b.Number) })
	i := slices.IndexFunc(majors, func(m MacOSMajor) bool { return m.Number == major })
	switch {
	case major > s.Newest.Number:
		s.Note = fmt.Sprintf("o macOS %d é mais novo que o catálogo do SOFA (que vai até o %d): beta, ou catálogo desatualizado", major, s.Newest.Number)
		return s
	case i < 0 && major < oldest.Number:
		s.State = SupportEnded
		s.Note = fmt.Sprintf("o macOS %d é anterior a todas as majors do catálogo do SOFA (a mais antiga é a %d)", major, oldest.Number)
		return s
	case i < 0:
		s.Note = fmt.Sprintf("o macOS %d não está no catálogo do SOFA", major)
		return s
	}
	s.Major = majors[i]
	launch := s.Newest.Launched
	switch {
	case major == s.Newest.Number:
		s.State = Supported
		s.Note = "é a major mais nova"
	case !s.Major.LatestDate.Before(launch):
		s.State = Supported
		s.Note = fmt.Sprintf("recebeu a %s em %s, no ciclo do macOS %d (lançado em %s)",
			s.Major.Latest, day(s.Major.LatestDate), s.Newest.Number, day(launch))
	case newerMajors(majors, major) <= 2 && now.Sub(launch) < macOSSupportGrace:
		s.State = SupportUnconfirmed
		s.Note = fmt.Sprintf("o macOS %d saiu em %s e esta major ainda não recebeu versão de segurança desde então (a última é a %s, de %s); a Apple costuma atualizar as duas anteriores no mesmo ciclo",
			s.Newest.Number, day(launch), s.Major.Latest, day(s.Major.LatestDate))
	default:
		s.State = SupportEnded
		s.Note = fmt.Sprintf("a última versão de segurança é a %s, de %s, anterior ao lançamento do macOS %d (%s)",
			s.Major.Latest, day(s.Major.LatestDate), s.Newest.Number, day(launch))
	}
	return s
}

func day(t time.Time) string { return t.Format("2006-01-02") }

// newerMajors conta as majors do catálogo mais novas que esta. É a posição, não a
// diferença dos números: depois do 15 veio o 26 (a Apple passou a numerar pelo ano).
func newerMajors(majors []MacOSMajor, major int) int {
	n := 0
	for _, m := range majors {
		if m.Number > major {
			n++
		}
	}
	return n
}

// MacOSMajorOf devolve a major de uma versão ("26.6.2" → 26).
func MacOSMajorOf(v string) (int, bool) {
	first, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(first)
	return n, err == nil && n > 0
}

// MacOSResult é a avaliação de uma versão do macOS contra as correções da própria major.
type MacOSResult struct {
	Version string
	CVEs    int        // CVEs corrigidas na major, no catálogo
	Pending []MacOSCVE // a versão é menor que a menor correção da CVE na major
	Target  string     // a maior correção entre as pendentes
	Ahead   bool       // a versão é mais nova que a última versão de segurança da major
	Invalid []string   // versões que não puderam ser comparadas
}

// EvaluateMacOS compara a versão da máquina com as correções da major dela.
//
// Regras (Aula 6.4, parte 4):
//   - a comparação é dentro da major, pela versão (o comparador de pontos), nunca pelo
//     build: boa parte das versões não tem build no SOFA, e o 27.0.1 tem dois;
//   - a mesma CVE pode aparecer em mais de uma versão da major (26.6.1 e 26.7): está
//     pendente se a versão da máquina é menor que a da menor correção; o alvo é a maior;
//   - versão sem CVEs (27.0.1, 26.5.1) não gera pendência: o alvo não é "a última versão".
func EvaluateMacOS(machine, latest string, fixes []MacOSFix) MacOSResult {
	res := MacOSResult{Version: machine}
	if c, err := version.CompareDotted(machine, latest); err == nil && c > 0 {
		res.Ahead = true
	}
	byCVE := map[string]*MacOSCVE{}
	var order []string
	for _, f := range fixes {
		c, ok := byCVE[f.CVE]
		if !ok {
			c = &MacOSCVE{CVE: f.CVE, Min: f.Version, Max: f.Version, Released: f.Released}
			byCVE[f.CVE] = c
			order = append(order, f.CVE)
		}
		c.Exploited = c.Exploited || f.Exploited
		c.KEV = c.KEV || f.KEV
		if severityRank[strings.ToLower(f.Severity)] > severityRank[strings.ToLower(c.Severity)] {
			c.Severity = f.Severity
		}
		if lt, err := version.CompareDotted(f.Version, c.Min); err == nil && lt < 0 {
			c.Min, c.Released = f.Version, f.Released
		}
		if gt, err := version.CompareDotted(f.Version, c.Max); err == nil && gt > 0 {
			c.Max = f.Version
		}
	}
	res.CVEs = len(byCVE)
	for _, id := range order {
		c := byCVE[id]
		r, err := version.CompareDotted(machine, c.Min)
		if err != nil {
			res.Invalid = append(res.Invalid, fmt.Sprintf("%s × %s (%s)", machine, c.Min, c.CVE))
			continue
		}
		if r >= 0 {
			continue
		}
		res.Pending = append(res.Pending, *c)
		if res.Target == "" {
			res.Target = c.Max
		} else if t, err := version.CompareDotted(c.Max, res.Target); err == nil && t > 0 {
			res.Target = c.Max
		}
	}
	SortMacOSCVEs(res.Pending)
	return res
}

// SortMacOSCVEs ordena: KEV, depois explorada pela Apple, depois severidade e ID.
func SortMacOSCVEs(list []MacOSCVE) {
	rank := func(c MacOSCVE) int { return 2*boolInt(c.KEV) + boolInt(c.Exploited && !c.KEV) }
	slices.SortFunc(list, func(a, b MacOSCVE) int {
		return cmp.Or(
			-cmp.Compare(rank(a), rank(b)),
			-cmp.Compare(severityRank[strings.ToLower(a.Severity)], severityRank[strings.ToLower(b.Severity)]),
			strings.Compare(a.CVE, b.CVE))
	})
}

// MacOSHardware resume o que o modelo permite, dada a situação de suporte da major da
// máquina. Devolve um rótulo curto e a explicação.
//   - major encerrada e o modelo aceita uma major com suporte: "ATUALIZAR A MAJOR";
//   - major encerrada e a major máxima do modelo também: "HARDWARE FORA DE SUPORTE";
//   - major com suporte, que é a máxima do modelo, mas não a mais nova: aviso;
//   - major com suporte e o modelo aceita uma mais nova: informação (não é pendência).
func MacOSHardware(model MacModel, machineMajor int, sup MacOSSupport, majors []MacOSMajor, now time.Time) (string, string) {
	top := model.MaxMajor()
	if top == 0 {
		return "", "o SOFA não informa as majors aceitas pelo modelo"
	}
	topSup := MacOSSupportOf(majors, top, now)
	switch {
	case sup.State == SupportEnded && top > machineMajor && topSup.State != SupportEnded:
		return "ATUALIZAR A MAJOR", fmt.Sprintf("o modelo aceita até o macOS %d, que tem suporte; nesta major não sai mais correção", top)
	case sup.State == SupportEnded:
		return "HARDWARE FORA DE SUPORTE", fmt.Sprintf("a major mais nova que o modelo aceita (macOS %d) não recebe mais atualizações de segurança", top)
	case top == machineMajor && machineMajor < sup.Newest.Number:
		return "AVISO", fmt.Sprintf("o modelo não passa do macOS %d: quando esta major sair de suporte, o hardware sai junto", top)
	case top > machineMajor:
		return "", fmt.Sprintf("o modelo aceita até o macOS %d (atualizar a major é opcional enquanto esta tiver suporte)", top)
	}
	return "", fmt.Sprintf("o modelo aceita até o macOS %d", top)
}

// MacExtra é uma melhoria de segurança em segundo plano ("26.3.1 (a)") reconhecida pelo
// build. O SOFA não traz as CVEs dela: o rótulo é só informação, não conta como correção.
type MacExtra struct {
	Build        string // 25D771280a
	Version      string // 26.3.1
	Extra        string // (a)
	Prerequisite string // 25D2128: o build sobre o qual ela se instala
}
