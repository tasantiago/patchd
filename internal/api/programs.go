package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// Inventário › Programas instalados: o software do parque num lugar só, separado por sistema
// operacional (Windows, Linux, macOS). Cada programa junta as entradas do mesmo nome sem a
// versão e a arquitetura ("LibreOffice 25.2.1.1" e "LibreOffice 24.2.5.2" são o LibreOffice) e
// do mesmo fabricante; a página dele mostra as versões e as máquinas. A lista vem do inventário
// atual de cada máquina e é guardada por programsTTL (uma consulta pesada: o jsonb de todo o
// parque).

// programsTTL: quanto a lista do parque vale.
const programsTTL = 10 * time.Minute

// programOS: os sistemas, na ordem das abas (o os_family do inventário).
var programOS = []struct{ ID, Label string }{{"windows", "Windows"}, {"linux", "Linux"}, {"darwin", "macOS"}}

// programsPerPage: programas (e máquinas, na página do programa) por página.
const programsPerPage = 50

// fleetProgram é um programa do parque, num sistema.
type fleetProgram struct {
	OS, Key, Name, Publisher string
	Installs                 int            // instalações (uma máquina com duas versões conta duas)
	Versions                 map[string]int // versão → instalações
	Names                    []string       // os nomes exatos do inventário
	System                   bool           // todas as entradas são componente do sistema ou atualização
	Silent                   int            // instalações com desinstalação silenciosa registrada (Windows)
	User                     int            // instalações por usuário
}

type programsCache struct {
	once sync.Once
	sem  chan struct{}

	mu   sync.Mutex
	at   time.Time
	list []fleetProgram
}

func (c *programsCache) init() { c.once.Do(func() { c.sem = make(chan struct{}, 1) }) }

func (c *programsCache) fresh(now time.Time) ([]fleetProgram, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil && now.Sub(c.at) < programsTTL && !now.Before(c.at) {
		return c.list, true
	}
	return nil, false
}

// fleetPrograms: a lista guardada, ou uma nova (um cálculo por vez; quem chega no meio espera).
func (a *api) fleetPrograms(ctx context.Context) ([]fleetProgram, time.Time, error) {
	c := &a.programs
	c.init()
	if l, ok := c.fresh(a.now()); ok {
		return l, c.at, nil
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, time.Time{}, ctx.Err()
	}
	defer func() { <-c.sem }()
	if l, ok := c.fresh(a.now()); ok {
		return l, c.at, nil
	}
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), complianceTimeout)
	defer cancel()
	rows, err := a.store.FleetSoftware(work)
	if err != nil {
		return nil, time.Time{}, err
	}
	l := aggregatePrograms(rows)
	c.mu.Lock()
	c.list, c.at = l, a.now()
	c.mu.Unlock()
	return l, c.at, nil
}

var (
	// A versão no nome: "25.2.1.1", "v8.9", "(24.09)", "- 14.38.33135".
	progVersion = regexp.MustCompile(`(?i)\s*[-–(]?\s*\bv?\d+(\.\d+)+([a-z0-9.+-]*)\)?`)
	// A arquitetura no nome: "(x64)", "(64-bit)", "(x64 edition)", "(x86 pt-BR)"...
	progArch   = regexp.MustCompile(`(?i)\s*\((x64|x86|amd64|arm64|64-bit|32-bit|64 bits|32 bits)[^)]*\)`)
	progSpaces = regexp.MustCompile(`\s+`)
)

// programName: o nome do programa sem a versão e a arquitetura ("7-Zip 24.09 (x64)" → "7-Zip").
// Sem nada que sobre, o nome inteiro.
func programName(name string) string {
	n := progArch.ReplaceAllString(name, "")
	n = progVersion.ReplaceAllString(n, "")
	n = strings.Trim(progSpaces.ReplaceAllString(n, " "), " -–")
	if n == "" {
		return strings.TrimSpace(name)
	}
	return n
}

// aggregatePrograms junta as combinações do banco por sistema, programa e fabricante.
func aggregatePrograms(rows []store.SoftwareRow) []fleetProgram {
	by := map[string]*fleetProgram{}
	best := map[string]int{} // o nome mostrado: o da combinação com mais instalações
	for _, r := range rows {
		name := programName(r.Name)
		key := strings.ToLower(name)
		id := r.OSFamily + "\x00" + key + "\x00" + r.Publisher
		p := by[id]
		if p == nil {
			p = &fleetProgram{OS: r.OSFamily, Key: key, Name: name, Publisher: r.Publisher, Versions: map[string]int{}, System: true}
			by[id] = p
		}
		if r.Machines > best[id] {
			best[id], p.Name = r.Machines, name
		}
		p.Installs += r.Machines
		p.Versions[r.Version] += r.Machines
		if !slices.Contains(p.Names, r.Name) {
			p.Names = append(p.Names, r.Name)
		}
		p.System = p.System && r.System
		if r.Uninstall != "" {
			p.Silent += r.Machines
		}
		if r.Scope == "user" {
			p.User += r.Machines
		}
	}
	out := make([]fleetProgram, 0, len(by))
	for _, p := range by {
		slices.Sort(p.Names)
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b fleetProgram) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(strings.ToLower(a.Publisher), strings.ToLower(b.Publisher)), cmp.Compare(a.OS, b.OS))
	})
	return out
}

// versionsText: as versões mais comuns primeiro ("26.8.3.1 (120), 25.2.1.1 (40) e mais 3").
func versionsText(v map[string]int, max int) string {
	type vc struct {
		v string
		n int
	}
	var l []vc
	for k, n := range v {
		l = append(l, vc{k, n})
	}
	slices.SortFunc(l, func(a, b vc) int { return cmp.Or(cmp.Compare(b.n, a.n), compareTPVersion(b.v, a.v)) })
	var parts []string
	for _, x := range l[:min(len(l), max)] {
		parts = append(parts, fmt.Sprintf("%s (%d)", dash(x.v), x.n))
	}
	s := strings.Join(parts, ", ")
	if len(l) > max {
		s += fmt.Sprintf(" e mais %d", len(l)-max)
	}
	return s
}

func programOSLabel(id string) string {
	for _, o := range programOS {
		if o.ID == id {
			return o.Label
		}
	}
	return id
}

// programHref: a página do programa.
func programHref(osID, key, publisher string) string {
	return "/painel/inventario/programa?" + url.Values{"so": {osID}, "nome": {key}, "fab": {publisher}}.Encode()
}

type programTab struct {
	Label, Href string
	Count       int
	Active      bool
}

type programRow struct {
	Name, Publisher, Href, Versions, Removal string
	Installs, VersionCount                   int
	System                                   bool
}

type programsView struct {
	pageBase
	OS, OSLabel   string
	Tabs          []programTab
	Q             string
	System        bool // ?sistema=1: também os componentes do sistema e as atualizações
	Rows          []programRow
	Found, Hidden int // encontrados; componentes do sistema fora da lista
	Shown         string
	Prev, Next    string
	SystemHref    string
	At, ClearHref string
	Unavailable   string
}

// programsPage: GET /painel/inventario/programas?so=windows&q=&sistema=1&pagina=N.
func (a *api) programsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	osID := q.Get("so")
	if programOSLabel(osID) == osID {
		osID = "windows"
	}
	search := texto.Clip(strings.TrimSpace(q.Get("q")), maxSearchLen)
	system := q.Get("sistema") == "1"
	v := programsView{pageBase: a.base(r, "Programas instalados", "inventario"), OS: osID, OSLabel: programOSLabel(osID), Q: search, System: system}
	link := func(o string, sys bool, s string, page int) string {
		u := url.Values{"so": {o}}
		if s != "" {
			u.Set("q", s)
		}
		if sys {
			u.Set("sistema", "1")
		}
		if page > 1 {
			u.Set("pagina", strconv.Itoa(page))
		}
		return "/painel/inventario/programas?" + u.Encode()
	}
	v.SystemHref, v.ClearHref = link(osID, !system, search, 1), link(osID, system, "", 1)
	list, at, err := a.fleetPrograms(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os programas do parque", err)
		return
	}
	v.At = a.stamp(at)
	counts := map[string]int{}
	needle := strings.ToLower(search)
	var shown []fleetProgram
	for _, p := range list {
		if p.System && !system {
			if p.OS == osID {
				v.Hidden++
			}
			continue
		}
		counts[p.OS]++
		if p.OS != osID {
			continue
		}
		if needle != "" && !strings.Contains(p.Key, needle) && !strings.Contains(strings.ToLower(p.Publisher), needle) {
			continue
		}
		shown = append(shown, p)
	}
	for _, o := range programOS {
		v.Tabs = append(v.Tabs, programTab{Label: o.Label, Href: link(o.ID, system, search, 1), Count: counts[o.ID], Active: o.ID == osID})
	}
	v.Found = len(shown)
	page, pages := pageOf(q.Get("pagina"), len(shown), programsPerPage)
	from, to := (page-1)*programsPerPage, min(page*programsPerPage, len(shown))
	if len(shown) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(shown))
	}
	if page > 1 {
		v.Prev = link(osID, system, search, page-1)
	}
	if page < pages {
		v.Next = link(osID, system, search, page+1)
	}
	for _, p := range shown[from:to] {
		row := programRow{Name: p.Name, Publisher: dash(p.Publisher), Href: programHref(p.OS, p.Key, p.Publisher), Installs: p.Installs,
			Versions: versionsText(p.Versions, 3), VersionCount: len(p.Versions), System: p.System}
		if p.OS == "windows" {
			switch {
			case p.Silent == p.Installs:
				row.Removal = "silenciosa"
			case p.Silent > 0:
				row.Removal = fmt.Sprintf("silenciosa em %d de %d", p.Silent, p.Installs)
			default:
				row.Removal = "sem desinstalação silenciosa"
			}
		}
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "programas", v)
}

type programVersion struct {
	Version  string
	Installs int
}

type programMachine struct {
	store.SoftwareInstall
	Href, Scope, Removal string
	Removable            bool // instalação de máquina com desinstalação silenciosa
}

type programView struct {
	pageBase
	OS, OSLabel     string
	Name, Publisher string
	BackHref        string
	Installs        int
	Versions        []programVersion
	Names           []string
	Machines        []programMachine
	Shown           string
	Prev, Next      string
	At              string
	Silent, User    int
	NotFound        bool
	CanRemove       bool   // admin com a chave de jobs: o Remover de cada máquina
	Self            string // esta página (o voltar do formulário)
}

// removalLabel: como a entrada sai sem perguntar nada (Windows).
func removalLabel(method string) string {
	switch method {
	case "msi":
		return "MSI (msiexec /x)"
	case "quiet":
		return "silenciosa do fabricante"
	case "inno":
		return "Inno Setup (/VERYSILENT)"
	}
	return "sem desinstalação silenciosa"
}

// programPage: GET /painel/inventario/programa?so=windows&nome=libreoffice&fab=The Document Foundation.
func (a *api) programPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	osID, key, pub := q.Get("so"), q.Get("nome"), q.Get("fab")
	v := programView{pageBase: a.base(r, "Programa instalado", "inventario"), OS: osID, OSLabel: programOSLabel(osID),
		BackHref: "/painel/inventario/programas?" + url.Values{"so": {osID}}.Encode()}
	list, at, err := a.fleetPrograms(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os programas do parque", err)
		return
	}
	v.At = a.stamp(at)
	v.CanRemove = v.Admin && a.jobKey != nil && osID == "windows"
	v.Self = "/painel/inventario/programa?" + url.Values{"so": {osID}, "nome": {key}, "fab": {pub}, "pagina": {q.Get("pagina")}}.Encode()
	if n := q.Get("job"); q.Get("criado") != "" && n != "" {
		v.Flash = "Remoção pedida (job " + n + "). O agente remove no próximo check-in e manda o inventário; a máquina sai desta lista quando a lista do parque for recalculada (até 10 minutos)."
	}
	if q.Get("erro") == "remover" {
		v.Error = "A remoção não foi pedida: " + q.Get("motivo") + "."
	}
	i := slices.IndexFunc(list, func(p fleetProgram) bool { return p.OS == osID && p.Key == key && p.Publisher == pub })
	if i < 0 {
		v.NotFound = true
		a.render(w, r, http.StatusNotFound, "programa-instalado", v)
		return
	}
	p := list[i]
	v.Name, v.Publisher, v.Installs, v.Names, v.Silent, v.User = p.Name, p.Publisher, p.Installs, p.Names, p.Silent, p.User
	for ver, n := range p.Versions {
		v.Versions = append(v.Versions, programVersion{Version: dash(ver), Installs: n})
	}
	slices.SortFunc(v.Versions, func(a, b programVersion) int { return compareTPVersion(b.Version, a.Version) })
	installs, err := a.store.SoftwareInstalls(r.Context(), osID, p.Names, p.Publisher)
	if err != nil {
		a.internalError(w, r, "listar as máquinas do programa", err)
		return
	}
	page, pages := pageOf(q.Get("pagina"), len(installs), programsPerPage)
	from, to := (page-1)*programsPerPage, min(page*programsPerPage, len(installs))
	if len(installs) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(installs))
	}
	at2 := func(n int) string {
		c := url.Values{"so": {osID}, "nome": {key}, "fab": {pub}, "pagina": {strconv.Itoa(n)}}
		return "/painel/inventario/programa?" + c.Encode()
	}
	if page > 1 {
		v.Prev = at2(page - 1)
	}
	if page < pages {
		v.Next = at2(page + 1)
	}
	for _, in := range installs[from:to] {
		m := programMachine{SoftwareInstall: in, Href: "/painel/maquinas/" + url.PathEscape(in.MachineID) + "?aba=software"}
		m.Scope = "máquina"
		if in.Scope == "user" {
			m.Scope = "usuário"
		}
		if osID == "windows" {
			m.Removal = removalLabel(in.Uninstall)
			m.Removable = removableEntry(protocol.Software{Name: in.Name, Version: in.Version, Scope: in.Scope, Uninstall: in.Uninstall,
				Source: in.Source, ProductCode: in.ProductCode}) == nil
		}
		v.Machines = append(v.Machines, m)
	}
	a.render(w, r, http.StatusOK, "programa-instalado", v)
}
