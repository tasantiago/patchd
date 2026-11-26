package api

import (
	"bytes"
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

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/jobs"
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
	CSVHref       string // a lista inteira em CSV, com o mesmo filtro
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
	v.CSVHref = programsCSVHref(osID, search, system)
	shown, counts, hidden := filterPrograms(list, osID, search, system)
	v.Hidden = hidden
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
		v.Rows = append(v.Rows, programRow{Name: p.Name, Publisher: dash(p.Publisher), Href: programHref(p.OS, p.Key, p.Publisher), Installs: p.Installs,
			Versions: versionsText(p.Versions, 3), VersionCount: len(p.Versions), System: p.System, Removal: programRemoval(p)})
	}
	a.render(w, r, http.StatusOK, "programas", v)
}

// filterPrograms: os programas do sistema com a busca (no nome e no fabricante), como a tela
// mostra; counts, os de cada sistema (as abas); hidden, os componentes do sistema que ficaram
// fora (sem ?sistema=1).
func filterPrograms(list []fleetProgram, osID, search string, system bool) (shown []fleetProgram, counts map[string]int, hidden int) {
	counts = map[string]int{}
	needle := strings.ToLower(search)
	for _, p := range list {
		if p.System && !system {
			if p.OS == osID {
				hidden++
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
	return shown, counts, hidden
}

// programRemoval: como o programa sai, no Windows ("" nos outros sistemas).
func programRemoval(p fleetProgram) string {
	switch {
	case p.OS != "windows":
		return ""
	case p.Silent == p.Installs:
		return "silenciosa"
	case p.Silent > 0:
		return fmt.Sprintf("silenciosa em %d de %d", p.Silent, p.Installs)
	}
	return "sem desinstalação silenciosa"
}

// programsCSVHref: o CSV da lista com o mesmo filtro da tela.
func programsCSVHref(osID, search string, system bool) string {
	u := url.Values{"so": {osID}}
	if search != "" {
		u.Set("q", search)
	}
	if system {
		u.Set("sistema", "1")
	}
	return "/painel/inventario/programas.csv?" + u.Encode()
}

// programsCSV: GET /painel/inventario/programas.csv?so=&q=&sistema=1, a lista inteira (sem
// páginas) com o filtro da tela, um programa por linha, com todas as versões.
func (a *api) programsCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	osID := q.Get("so")
	if programOSLabel(osID) == osID {
		osID = "windows"
	}
	search := texto.Clip(strings.TrimSpace(q.Get("q")), maxSearchLen)
	system := q.Get("sistema") == "1"
	list, at, err := a.fleetPrograms(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os programas do parque", err)
		return
	}
	shown, _, _ := filterPrograms(list, osID, search, system)
	rows := make([][]string, 0, len(shown))
	for _, p := range shown {
		versions := make([]string, 0, len(p.Versions))
		for ver := range p.Versions {
			versions = append(versions, ver)
		}
		slices.SortFunc(versions, func(a, b string) int { return compareTPVersion(b, a) })
		for i, ver := range versions {
			versions[i] = fmt.Sprintf("%s (%d)", cmp.Or(ver, "sem versão"), p.Versions[ver])
		}
		row := []string{programOSLabel(p.OS), p.Name, p.Publisher, strconv.Itoa(p.Installs), strconv.Itoa(len(p.Versions)),
			strings.Join(versions, ", "), strconv.Itoa(p.User), yesNo(p.System), strings.Join(p.Names, " | ")}
		if osID == "windows" {
			row = append(row, strconv.Itoa(p.Silent), programRemoval(p))
		}
		rows = append(rows, row)
	}
	header := []string{"sistema", "programa", "fabricante", "instalacoes", "qtd_versoes", "versoes", "por_usuario", "componente_do_sistema", "nomes_no_inventario"}
	if osID == "windows" {
		header = append(header, "com_desinstalacao_silenciosa", "remocao")
	}
	suffix := ""
	if search != "" {
		suffix = "-busca"
	}
	name := fmt.Sprintf("patchd-programas-%s%s-%s.csv", osID, suffix, at.UTC().Format("20060102-1504"))
	a.sendCSV(w, r, name, len(rows), func(b *bytes.Buffer) error { return export.Table(b, header, rows) })
}

// programCSV: GET /painel/inventario/programa.csv?so=&nome=&fab=, as instalações do programa,
// uma por linha (todas, sem páginas).
func (a *api) programCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	osID, key, pub := q.Get("so"), q.Get("nome"), q.Get("fab")
	list, at, err := a.fleetPrograms(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os programas do parque", err)
		return
	}
	i := slices.IndexFunc(list, func(p fleetProgram) bool { return p.OS == osID && p.Key == key && p.Publisher == pub })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	p := list[i]
	installs, err := a.store.SoftwareInstalls(r.Context(), osID, p.Names, p.Publisher)
	if err != nil {
		a.internalError(w, r, "listar as máquinas do programa", err)
		return
	}
	header := []string{"maquina", "id", "programa", "fabricante", "nome_no_inventario", "versao", "escopo", "usuario"}
	if osID == "windows" {
		header = append(header, "remocao", "removivel_pelo_patchd", "product_code", "chave")
	}
	rows := make([][]string, 0, len(installs))
	for _, in := range installs {
		scope := "máquina"
		if in.Scope == "user" {
			scope = "usuário"
		}
		row := []string{in.Hostname, in.MachineID, p.Name, p.Publisher, in.Name, in.Version, scope, in.User}
		if osID == "windows" {
			ok := removableEntry(protocol.Software{Name: in.Name, Version: in.Version, Scope: in.Scope, Uninstall: in.Uninstall,
				Source: in.Source, ProductCode: in.ProductCode}) == nil
			row = append(row, removalLabel(in.Uninstall), yesNo(ok), in.ProductCode, in.Source)
		}
		rows = append(rows, row)
	}
	name := fmt.Sprintf("patchd-programa-%s-%s-%s.csv", osID, fileSlug(p.Name), at.UTC().Format("20060102-1504"))
	a.sendCSV(w, r, name, len(rows), func(b *bytes.Buffer) error { return export.Table(b, header, rows) })
}

// fileSlug: o nome do programa no nome do arquivo (só letras, números e hífen; até 40).
func fileSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return cmp.Or(strings.Trim(b.String(), "-"), "programa")
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
	// A campanha de remoção: o formulário (só admin, Windows, com a chave de jobs).
	Campaign *campaignForm
	Key      string // a chave do nome do programa (o campo remover_nome)
	CSVHref  string // as máquinas em CSV (todas)
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
	if q.Get("erro") == "campanha" {
		v.Error = "A campanha de remoção não foi criada: " + q.Get("motivo")
	}
	v.Key = key
	v.CSVHref = "/painel/inventario/programa.csv?" + url.Values{"so": {osID}, "nome": {key}, "fab": {pub}}.Encode()
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
	if v.CanRemove {
		f := campaignForm{Type: jobs.TypeUninstall, Name: "Remover " + p.Name, When: "horario", ObserveH: 24, TTLH: 24, Resends: resendChoices,
			Resend: 1, DailyStart: "02:00", DailyDur: 180, TZ: a.panelLocation().String()}
		o, err := a.deployOverview(r.Context())
		if err != nil {
			a.internalError(w, r, "listar os prédios", err)
			return
		}
		f.Scope = scopeFormFrom(o, scopeInput{All: true}, "")
		f.Observe, f.TTL = hourOptions(observeHours, f.ObserveH), hourOptions(ttlHours, f.TTLH)
		for _, m := range windowDurations {
			f.Durations = append(f.Durations, durationOption{Minutes: m, Label: durationLabel(m)})
		}
		v.Campaign = &f
	}
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

// removeTargetFromForm: o programa da campanha de remoção, achado na lista do parque pelo que
// o formulário diz (o sistema, a chave do nome e o fabricante); os nomes exatos vêm da lista,
// não do formulário. As versões marcadas; todas marcadas: todas (também as que aparecerem).
func (a *api) removeTargetFromForm(r *http.Request, f *campaignForm) (*campaign.RemoveTarget, string) {
	f.RemoveOS, f.RemoveKey, f.RemovePub = r.PostFormValue("remover_so"), r.PostFormValue("remover_nome"), r.PostFormValue("remover_fab")
	if f.RemoveOS != "windows" {
		return nil, "A remoção pelo patchd é só no Windows."
	}
	list, _, err := a.fleetPrograms(r.Context())
	if err != nil {
		return nil, "Não foi possível ler os programas do parque: " + err.Error()
	}
	i := slices.IndexFunc(list, func(p fleetProgram) bool {
		return p.OS == f.RemoveOS && p.Key == f.RemoveKey && p.Publisher == f.RemovePub
	})
	if i < 0 {
		return nil, "O programa não está mais no inventário de nenhuma máquina."
	}
	p := list[i]
	var versions []string
	for _, v := range r.PostForm["versao"] {
		if _, ok := p.Versions[v]; ok && !slices.Contains(versions, v) {
			versions = append(versions, v)
		}
	}
	f.RemoveVersions = versions
	if len(versions) == 0 {
		return nil, "Marque ao menos uma versão a remover."
	}
	if len(versions) == len(p.Versions) {
		versions = nil
	}
	slices.SortFunc(versions, func(a, b string) int { return compareTPVersion(a, b) })
	if len(p.Names) > campaign.MaxRemoveNames {
		return nil, fmt.Sprintf("O programa aparece com %d nomes diferentes no inventário; a campanha leva até %d. Remova por versão.", len(p.Names), campaign.MaxRemoveNames)
	}
	return &campaign.RemoveTarget{Program: p.Name, Publisher: p.Publisher, Names: slices.Clone(p.Names), Versions: versions}, ""
}

// removePreview: em cada etapa, quantas máquinas têm o programa e quantas o patchd consegue
// remover agora (instalação de máquina com desinstalação silenciosa).
func (a *api) removePreview(r *http.Request, f campaignForm, c campaign.Campaign, groups map[int][]string) (map[int]string, error) {
	installs, err := a.store.SoftwareInstalls(r.Context(), f.RemoveOS, c.Remove.Names, c.Remove.Publisher)
	if err != nil {
		return nil, err
	}
	stage := map[string]int{}
	for idx, ids := range groups {
		for _, id := range ids {
			stage[id] = idx
		}
	}
	type pair struct{ has, can map[string]bool }
	by := map[int]*pair{}
	for _, in := range installs {
		sw := protocol.Software{Name: in.Name, Version: in.Version, Publisher: c.Remove.Publisher, Scope: in.Scope, Uninstall: in.Uninstall,
			Source: in.Source, ProductCode: in.ProductCode}
		if !c.Remove.Matches(sw) {
			continue
		}
		idx, ok := stage[in.MachineID]
		if !ok {
			continue
		}
		if by[idx] == nil {
			by[idx] = &pair{map[string]bool{}, map[string]bool{}}
		}
		by[idx].has[in.MachineID] = true
		if removableEntry(sw) == nil {
			by[idx].can[in.MachineID] = true
		}
	}
	out := map[int]string{}
	for idx := range groups {
		p := by[idx]
		if p == nil {
			out[idx] = "nenhuma máquina com o programa"
			continue
		}
		out[idx] = fmt.Sprintf("%d com o programa · %d removível(is) pelo patchd", len(p.has), len(p.can))
	}
	return out, nil
}
