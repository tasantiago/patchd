package thirdparty

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/version"
)

// Ref é a versão de referência de uma regra, buscada na fonte pelo catalog sync.
type Ref struct {
	App     string // id da regra
	OS      string
	Version string
	Source  string // chrome:win64, winget:7zip.7zip...
	Fetched time.Time
	// CheckVersion e CheckSource: a da segunda fonte (App.Check), quando houver.
	CheckVersion, CheckSource string
}

// Data é o que a avaliação cruza com o inventário: as versões de referência e os ciclos de
// suporte do endoflife.date, no instante da avaliação.
type Data struct {
	Refs   []Ref
	Cycles []Cycle
	Now    time.Time
}

// Situações de um programa identificado.
const (
	StateOutdated     = "DESATUALIZADO"    // abaixo da versão de referência (feed)
	StateBelowMinimum = "ABAIXO DO MÍNIMO" // abaixo da min_version (minimum)
	StateEOL          = "FORA DE SUPORTE"  // eol
	StateBadVersion   = "VERSÃO ILEGÍVEL"  // a versão do inventário (ou da referência) não é comparável
	StateNoReference  = "SEM REFERÊNCIA"   // feed sem versão guardada: rode o catalog sync
	StateOSVendor     = "CATÁLOGO DO SO"   // os-vendor
	StateCurrent      = "em dia"
)

var stateOrder = map[string]int{
	StateOutdated: 0, StateBelowMinimum: 1, StateEOL: 2, StateBadVersion: 3, StateNoReference: 4, StateOSVendor: 5, StateCurrent: 6,
}

// Finding é um programa do inventário reconhecido por uma regra.
type Finding struct {
	App       App
	Software  protocol.Software
	Installed string // versão normalizada (vazia se ilegível)
	Reference string // versão de referência ou mínima (vazia quando não se aplica)
	Ref       *Ref   // de onde veio a referência (feed)
	State     string
	// Cycle: o ciclo de suporte da versão instalada (regras com lifecycle); EOLSoon: o
	// suporte dele termina em LifecycleWarning.
	Cycle   *Cycle
	EOLSoon bool
	// Behind: a fonte principal está atrás da segunda (o winget-pkgs ainda não tem a versão
	// que o fabricante publicou); a referência é a da segunda.
	Behind bool
}

// Result é a avaliação dos programas de terceiros de uma máquina.
type Result struct {
	Rules     int                 // regras do manifesto para o SO
	Programs  int                 // programas no inventário (sem componentes do sistema e atualizações)
	Findings  []Finding           // identificados (sem os auxiliares)
	Auxiliary int                 // entradas auxiliares reconhecidas (categoria ignore)
	Unmatched []protocol.Software // programas sem regra
}

// Count conta os programas identificados numa situação.
func (r Result) Count(state string) int {
	n := 0
	for _, f := range r.Findings {
		if f.State == state {
			n++
		}
	}
	return n
}

// Evaluate reconhece os programas do inventário pelas regras do SO e compara as versões,
// sem os ciclos de suporte (EvaluateWith).
func Evaluate(osID string, sw []protocol.Software, m Manifest, refs []Ref) Result {
	return EvaluateWith(osID, sw, m, Data{Refs: refs, Now: time.Now()})
}

// EvaluateWith reconhece os programas do inventário pelas regras do SO e compara as versões.
// Cada entrada do inventário é avaliada pela primeira regra que a reconhece; duas entradas
// do mesmo programa (o 7-Zip instalado pelo exe e pelo MSI) são avaliadas separadamente.
func EvaluateWith(osID string, sw []protocol.Software, m Manifest, d Data) Result {
	rules := m.For(osID)
	res := Result{Rules: len(rules)}
	byKey := map[string]Ref{}
	for _, r := range d.Refs {
		byKey[r.App+"/"+r.OS] = r
	}
	cycles := map[string][]Cycle{}
	for _, c := range d.Cycles {
		cycles[c.Product] = append(cycles[c.Product], c)
	}
	for _, s := range sw {
		if s.SystemComponent || s.IsUpdate {
			continue
		}
		res.Programs++
		i := slices.IndexFunc(rules, func(a App) bool { return a.Matches(s.Name, s.Publisher) })
		if i < 0 {
			res.Unmatched = append(res.Unmatched, s)
			continue
		}
		a := rules[i]
		if a.Category == CategoryIgnore {
			res.Auxiliary++
			continue
		}
		f := Finding{App: a, Software: s}
		installed, ok := a.InstalledVersion(s.Name, s.Version)
		if ok {
			f.Installed = installed
		}
		switch a.Category {
		case CategoryOSVendor:
			f.State = StateOSVendor
		case CategoryEOL:
			f.State = StateEOL
		case CategoryMinimum:
			f.Reference, _ = Normalize(a.MinVersion)
			f.State = compare(installed, ok, f.Reference, StateBelowMinimum)
		case CategoryFeed:
			r, found := byKey[a.Key()]
			if !found {
				f.State = StateNoReference
				break
			}
			f.Ref = &r
			ref, rok := Normalize(r.Version)
			if !rok {
				f.State = StateBadVersion
				break
			}
			if cv, cok := Normalize(r.CheckVersion); cok {
				if c, err := version.CompareDotted(cv, ref); err == nil && c > 0 {
					ref, f.Behind = cv, true
				}
			}
			f.Reference = ref
			f.State = compare(installed, ok, ref, StateOutdated)
		case CategoryLifecycle:
			cy := CycleFor(installed, cycles[a.Lifecycle])
			if !ok || cy == nil {
				f.State = StateNoReference
				if !ok {
					f.State = StateBadVersion
				}
				break
			}
			ref, rok := Normalize(cy.Latest)
			if !rok {
				f.State = StateNoReference
				break
			}
			f.Reference = ref
			f.State = compare(installed, ok, ref, StateOutdated)
		}
		// O ciclo sem suporte vence: não vai haver correção nenhuma para ele.
		if a.Lifecycle != "" && ok {
			if cy := CycleFor(installed, cycles[a.Lifecycle]); cy != nil {
				f.Cycle = cy
				switch {
				case cy.EOLAt(d.Now):
					f.State = StateEOL
				case cy.EOLSoon(d.Now, LifecycleWarning):
					f.EOLSoon = true
				}
			}
		}
		res.Findings = append(res.Findings, f)
	}
	slices.SortStableFunc(res.Findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(stateOrder[a.State], stateOrder[b.State]), strings.Compare(strings.ToLower(a.Software.Name), strings.ToLower(b.Software.Name)))
	})
	slices.SortFunc(res.Unmatched, func(a, b protocol.Software) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return res
}

// compare devolve below quando a versão instalada é menor que a de referência.
func compare(installed string, ok bool, ref, below string) string {
	if !ok {
		return StateBadVersion
	}
	c, err := version.CompareDotted(installed, ref)
	switch {
	case err != nil:
		return StateBadVersion
	case c < 0:
		return below
	}
	return StateCurrent
}
