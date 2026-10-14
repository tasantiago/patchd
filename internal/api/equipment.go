package api

import (
	"bytes"
	"cmp"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/version"
)

// Inventário › Equipamentos (Aula 12.2): a frota como o inventário a vê, no formato das
// listas do ITOM: busca por campo, ordem por coluna, páginas de 15, 25 ou 50, e CSV com o
// mesmo filtro. A Frota continua sendo a visão do compliance.
//
// Filtro, ordem e páginas são feitos aqui, em Go, sobre a frota inteira: uma consulta
// (InventoryList) traz 1500 máquinas em milissegundos, e o IP principal sai do estado
// (Aula 12.1), que o banco não sabe escolher. Com dezenas de milhares de máquinas, o
// filtro e a página iriam para o SQL.

// onlineWithin: sem check-in há mais que isso, a máquina aparece offline. O check-in é de
// hora em hora (±10%); dois perdidos seguidos já dizem que a máquina não está na rede.
const onlineWithin = 2 * time.Hour

// Páginas: as opções do ITOM, e 25 de partida.
var perPageOptions = []int{15, 25, 50}

const (
	defaultPerPage = 25
	maxSearchLen   = 100
)

// Campos da busca: a chave vai na URL (?campo=); "todos" procura em todos.
var searchFields = []struct{ Key, Label string }{
	{"todos", "Todos os campos"}, {"nome", "Nome"}, {"ip", "IP"}, {"serial", "Serial"},
	{"so", "Sistema"}, {"modelo", "Fabricante e modelo"}, {"agente", "Versão do agente"}, {"categoria", "Categoria"},
	{"usuario", "Usuário conectado"},
}

// Colunas da tabela, na ordem; a chave vai na URL (?ordem=).
var equipColumns = []struct{ Key, Label string }{
	{"nome", "Nome"}, {"status", "Status"}, {"agente", "Agente"}, {"ip", "IP"}, {"usuario", "Usuário"},
	{"inventario", "Inventário"}, {"contato", "Último contato"}, {"canal", "Canal"},
	{"so", "Sistema"}, {"categoria", "Categoria"}, {"serial", "Serial"}, {"anel", "Anel"},
}

// Filtros de status (os cartões do topo).
const (
	statusOnline  = "online"
	statusOffline = "offline"
	statusNoState = "sem-estado"
)

// --- Categoria pelo hardware -------------------------------------------------------

// Tipos de chassi do SMBIOS (DMTF DSP0134, 7.4.1), agrupados como o ITOM mostra.
var chassisCategory = map[string]string{
	"3": "Desktop", "4": "Desktop", "5": "Desktop", "6": "Desktop", "7": "Desktop", "13": "Desktop",
	"15": "Desktop", "16": "Desktop", "24": "Desktop", "34": "Desktop", "35": "Desktop", "36": "Desktop",
	"8": "Notebook", "9": "Notebook", "10": "Notebook", "14": "Notebook", "31": "Notebook", "32": "Notebook",
	"30": "Tablet",
	"17": "Servidor", "23": "Servidor", "25": "Servidor", "28": "Servidor", "29": "Servidor",
}

// virtualHints: o fabricante ou o modelo que os hipervisores gravam no SMBIOS da VM. Vem
// antes do chassi: a VM do VMware diz chassi 1 (outro) e a do Hyper-V diz 3 (desktop).
var virtualHints = []string{"vmware", "virtualbox", "innotek", "qemu", "kvm", "xen", "parallels", "bochs", "virtual machine"}

// category: Virtual, Desktop, Notebook, Tablet, Servidor, Outro ou "" (não informado).
func category(manufacturer, model, chassis string) string {
	hw := strings.ToLower(manufacturer + " " + model)
	for _, h := range virtualHints {
		if strings.Contains(hw, h) {
			return "Virtual"
		}
	}
	// macOS: o modelo diz a família (MacBookPro18,1, iMac21,1...). "Mac14,2" pode ser
	// notebook ou desktop: fica sem categoria, em vez de um palpite.
	switch {
	case strings.HasPrefix(model, "MacBook"):
		return "Notebook"
	case strings.HasPrefix(model, "iMac"), strings.HasPrefix(model, "Macmini"), strings.HasPrefix(model, "MacPro"):
		return "Desktop"
	}
	if c, ok := chassisCategory[strings.TrimSpace(chassis)]; ok {
		return c
	}
	if chassis == "" || chassis == "1" || chassis == "2" { // vazio, "outro", "desconhecido"
		return ""
	}
	return "Outro"
}

// --- A lista ------------------------------------------------------------------------

// equip é uma máquina na lista: os valores mostrados e as chaves de ordem.
type equip struct {
	ID, Name, OS, Agent, IP, Channel, Category, Serial, Manufacturer, Model string
	IPFromCheckin                                                           bool // sem estado: o IP é o que o servidor viu
	Online, HasState                                                        bool
	Ring                                                                    int
	LastSeen                                                                time.Time
	Collected                                                               *time.Time
	ip                                                                      netip.Addr
	Users                                                                   []protocol.LoggedUser // Aula 15.6
}

// userLabel: como a conta aparece nas listas: o nome de exibição, ou a conta.
func userLabel(u protocol.LoggedUser) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Account
}

// userKey: o usuário da lista (o primeiro: ativo, de console), para ordenar e mostrar.
func (e equip) userKey() string {
	if len(e.Users) == 0 {
		return ""
	}
	return userLabel(e.Users[0])
}

func buildEquip(rows []store.InventoryRow, now time.Time) []equip {
	out := make([]equip, 0, len(rows))
	for _, r := range rows {
		e := equip{ID: r.ID, Name: r.Hostname, OS: strings.TrimSpace(r.OSName + " " + r.OSVersion), Agent: r.AgentVersion,
			Manufacturer: r.Manufacturer, Model: r.Model, Category: category(r.Manufacturer, r.Model, r.ChassisType),
			Ring: r.Ring, LastSeen: r.LastSeenAt, Collected: r.CollectedAt, Online: now.Sub(r.LastSeenAt) <= onlineWithin}
		if s, ok := identity.NormalizeSerial(r.Serial); ok {
			e.Serial = s
		}
		switch {
		case r.CheckinTLS == nil:
		case *r.CheckinTLS:
			e.Channel = "HTTPS"
		default:
			e.Channel = "HTTP"
		}
		if r.State != nil {
			e.HasState = true
			e.IP = r.State.PrimaryIPv4(r.CheckinAddr)
			e.Users = r.State.Users
		}
		if e.IP == "" && r.CheckinAddr != "" {
			e.IP, e.IPFromCheckin = r.CheckinAddr, true
		}
		e.ip, _ = netip.ParseAddr(e.IP)
		out = append(out, e)
	}
	return out
}

// equipQuery é o que a URL pede, já conferido.
type equipQuery struct {
	Field, Q, Status, Order string
	Desc                    bool
	Per, Page               int
}

func parseEquipQuery(v url.Values) equipQuery {
	q := equipQuery{Field: "todos", Order: "nome", Per: defaultPerPage, Page: 1}
	if f := v.Get("campo"); slices.ContainsFunc(searchFields, func(s struct{ Key, Label string }) bool { return s.Key == f }) {
		q.Field = f
	}
	q.Q = strings.TrimSpace(v.Get("q"))
	if len(q.Q) > maxSearchLen {
		q.Q = q.Q[:maxSearchLen]
	}
	if s := v.Get("status"); s == statusOnline || s == statusOffline || s == statusNoState {
		q.Status = s
	}
	if o := v.Get("ordem"); slices.ContainsFunc(equipColumns, func(c struct{ Key, Label string }) bool { return c.Key == o }) {
		q.Order = o
	}
	q.Desc = v.Get("dir") == "desc"
	if n, _ := strconv.Atoi(v.Get("por")); slices.Contains(perPageOptions, n) {
		q.Per = n
	}
	if n, _ := strconv.Atoi(v.Get("pagina")); n > 1 {
		q.Page = n
	}
	return q
}

// values é a URL canônica da consulta (sem os valores de partida).
func (q equipQuery) values() url.Values {
	v := url.Values{}
	if q.Q != "" {
		v.Set("q", q.Q)
		if q.Field != "todos" {
			v.Set("campo", q.Field)
		}
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Order != "nome" {
		v.Set("ordem", q.Order)
	}
	if q.Desc {
		v.Set("dir", "desc")
	}
	if q.Per != defaultPerPage {
		v.Set("por", strconv.Itoa(q.Per))
	}
	if q.Page > 1 {
		v.Set("pagina", strconv.Itoa(q.Page))
	}
	return v
}

func (q equipQuery) href(path string) string {
	if v := q.values(); len(v) > 0 {
		return path + "?" + v.Encode()
	}
	return path
}

// texts: onde a busca procura, campo a campo.
func (e equip) texts(field string) []string {
	switch field {
	case "nome":
		return []string{e.Name, e.ID}
	case "ip":
		return []string{e.IP}
	case "serial":
		return []string{e.Serial}
	case "so":
		return []string{e.OS}
	case "modelo":
		return []string{e.Manufacturer + " " + e.Model}
	case "agente":
		return []string{e.Agent}
	case "categoria":
		return []string{e.Category}
	case "usuario":
		return e.userTexts()
	}
	return append([]string{e.Name, e.ID, e.IP, e.Serial, e.OS, e.Manufacturer + " " + e.Model, e.Agent, e.Category}, e.userTexts()...)
}

// userTexts: a conta (com e sem o domínio) e o nome de cada usuário conectado.
func (e equip) userTexts() []string {
	var out []string
	for _, u := range e.Users {
		out = append(out, u.Account, u.DisplayName)
	}
	return out
}

// filterEquip: status e busca (sem diferença de maiúsculas, em qualquer parte do texto).
func filterEquip(list []equip, q equipQuery) []equip {
	needle := strings.ToLower(q.Q)
	out := []equip{}
	for _, e := range list {
		switch q.Status {
		case statusOnline:
			if !e.Online {
				continue
			}
		case statusOffline:
			if e.Online {
				continue
			}
		case statusNoState:
			if e.HasState {
				continue
			}
		}
		if needle != "" && !slices.ContainsFunc(e.texts(q.Field), func(s string) bool { return strings.Contains(strings.ToLower(s), needle) }) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// emptyLast: valor vazio vai para o fim nas duas direções (uma coluna ordenada não
// começa com uma página de "—").
func emptyLast(a, b bool) (int, bool) {
	switch {
	case a && b:
		return 0, true
	case a:
		return 1, true
	case b:
		return -1, true
	}
	return 0, false
}

// compareEquip compara pela coluna; 0 quando iguais (o desempate é o nome).
func compareEquip(a, b equip, order string) int {
	text := func(x, y string) int { return cmp.Compare(strings.ToLower(x), strings.ToLower(y)) }
	switch order {
	case "status":
		return cmp.Compare(btoi(!a.Online), btoi(!b.Online))
	case "agente":
		if c, err := version.CompareSemver(a.Agent, b.Agent); err == nil {
			return c
		}
		return text(a.Agent, b.Agent)
	case "ip":
		return a.ip.Compare(b.ip)
	case "usuario":
		return text(a.userKey(), b.userKey())
	case "inventario":
		if a.Collected == nil || b.Collected == nil {
			return 0 // o vazio já foi para o fim em sortEquip
		}
		return a.Collected.Compare(*b.Collected)
	case "contato":
		return a.LastSeen.Compare(b.LastSeen)
	case "canal":
		return text(a.Channel, b.Channel)
	case "so":
		return text(a.OS, b.OS)
	case "categoria":
		return text(a.Category, b.Category)
	case "serial":
		return text(a.Serial, b.Serial)
	case "anel":
		return cmp.Compare(a.Ring, b.Ring)
	}
	return text(a.Name, b.Name)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isEmpty: a coluna não tem valor nessa máquina.
func (e equip) isEmpty(order string) bool {
	switch order {
	case "nome":
		return e.Name == ""
	case "agente":
		return e.Agent == ""
	case "ip":
		return !e.ip.IsValid()
	case "usuario":
		return len(e.Users) == 0
	case "inventario":
		return e.Collected == nil
	case "canal":
		return e.Channel == ""
	case "so":
		return e.OS == ""
	case "categoria":
		return e.Category == ""
	case "serial":
		return e.Serial == ""
	}
	return false
}

func sortEquip(list []equip, order string, desc bool) {
	slices.SortStableFunc(list, func(a, b equip) int {
		if c, ok := emptyLast(a.isEmpty(order), b.isEmpty(order)); ok && c != 0 {
			return c
		}
		c := compareEquip(a, b, order)
		if desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// --- A página -----------------------------------------------------------------------

type equipRow struct {
	ID, Name, Agent, IP, Collected, Seen, Channel, OS, Category, Serial, Ring string
	User, UserAll                                                             string // o primeiro usuário; todos, na dica
	UserMore                                                                  int    // quantos além do primeiro
	Online, HasState, IPFromCheckin                                           bool
}

type equipView struct {
	pageBase
	Total, Online, Offline, NoState  int
	Status                           string
	KPIAll, KPIOnline, KPIOff, KPINo string // links dos cartões (mantêm a busca)
	Fields                           []optView
	Q                                string
	Pers                             []optView
	Order, Dir                       string // a ordem atual, para o formulário
	Cols                             []colView
	Rows                             []equipRow
	Found                            int
	Shown                            string // "1–25 de 132"
	Prev, Next                       string
	Pages                            []pageLink
	CSV                              string
	HasFilter                        bool
}

type optView struct {
	Value, Label string
	Selected     bool
}

type colView struct {
	Label, Href, Arrow, Sort string // Sort: aria-sort (ascending, descending ou "")
}

type pageLink struct {
	N       int
	Href    string
	Current bool
	Gap     bool // "…" no lugar das páginas omitidas
}

// pageLinks: a primeira, a última e duas de cada lado da atual; o resto vira "…".
func pageLinks(q equipQuery, pages int) []pageLink {
	var out []pageLink
	last := 0
	for n := 1; n <= pages; n++ {
		if n != 1 && n != pages && (n < q.Page-2 || n > q.Page+2) {
			continue
		}
		if last != 0 && n > last+1 {
			out = append(out, pageLink{Gap: true})
		}
		p := q
		p.Page = n
		out = append(out, pageLink{N: n, Href: p.href("/painel/inventario"), Current: n == q.Page})
		last = n
	}
	return out
}

func (a *api) equipmentPage(w http.ResponseWriter, r *http.Request) {
	q := parseEquipQuery(r.URL.Query())
	v := equipView{pageBase: a.base(r, "Equipamentos", "inventario"), Q: q.Q, Status: q.Status, Order: q.Order}
	if q.Desc {
		v.Dir = "desc"
	}
	rows, err := a.store.InventoryList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar o inventário", err)
		return
	}
	now := a.now()
	all := buildEquip(rows, now)
	v.Total = len(all)
	for _, e := range all {
		if e.Online {
			v.Online++
		}
		if !e.HasState {
			v.NoState++
		}
	}
	v.Offline = v.Total - v.Online

	// Os cartões trocam o status e mantêm a busca; voltam para a primeira página.
	kpi := func(status string) string {
		k := q
		k.Status, k.Page = status, 1
		return k.href("/painel/inventario")
	}
	v.KPIAll, v.KPIOnline, v.KPIOff, v.KPINo = kpi(""), kpi(statusOnline), kpi(statusOffline), kpi(statusNoState)

	for _, f := range searchFields {
		v.Fields = append(v.Fields, optView{Value: f.Key, Label: f.Label, Selected: f.Key == q.Field})
	}
	for _, n := range perPageOptions {
		v.Pers = append(v.Pers, optView{Value: strconv.Itoa(n), Label: strconv.Itoa(n), Selected: n == q.Per})
	}
	for _, c := range equipColumns {
		s := q
		s.Order, s.Page = c.Key, 1
		cv := colView{Label: c.Label}
		if c.Key == q.Order {
			s.Desc = !q.Desc
			cv.Arrow, cv.Sort = "▲", "ascending"
			if q.Desc {
				cv.Arrow, cv.Sort = "▼", "descending"
			}
		} else {
			s.Desc = false
		}
		cv.Href = s.href("/painel/inventario")
		v.Cols = append(v.Cols, cv)
	}

	found := filterEquip(all, q)
	sortEquip(found, q.Order, q.Desc)
	v.Found, v.HasFilter = len(found), q.Q != "" || q.Status != ""
	csvQ := q
	csvQ.Page, csvQ.Per = 1, defaultPerPage
	v.CSV = csvQ.href("/painel/inventario.csv")

	pages := max(1, (len(found)+q.Per-1)/q.Per)
	if q.Page > pages {
		q.Page = pages
	}
	from := (q.Page - 1) * q.Per
	to := min(from+q.Per, len(found))
	if len(found) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(found))
	}
	if q.Page > 1 {
		p := q
		p.Page--
		v.Prev = p.href("/painel/inventario")
	}
	if q.Page < pages {
		p := q
		p.Page++
		v.Next = p.href("/painel/inventario")
	}
	if pages > 1 {
		v.Pages = pageLinks(q, pages)
	}
	for _, e := range found[from:to] {
		row := equipRow{ID: e.ID, Name: dash(e.Name), Agent: dash(e.Agent), IP: dash(e.IP), Channel: dash(e.Channel),
			OS: dash(e.OS), Category: dash(e.Category), Serial: dash(e.Serial), Ring: strconv.Itoa(e.Ring),
			Online: e.Online, HasState: e.HasState, IPFromCheckin: e.IPFromCheckin,
			Seen: ago(now.Sub(e.LastSeen)), Collected: "—"}
		if e.Collected != nil {
			row.Collected = a.stamp(*e.Collected)
		}
		row.User, row.UserAll = "—", usersSummary(e.Users)
		if len(e.Users) > 0 {
			row.User, row.UserMore = userLabel(e.Users[0]), len(e.Users)-1
		}
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "inventario", v)
}

// stamp: a hora no fuso do painel (PATCHD_TIMEZONE, Aula 11.1); sem fuso, em UTC e dito.
func (a *api) stamp(t time.Time) string {
	if a.timezone != "" {
		if loc, err := time.LoadLocation(a.timezone); err == nil {
			return t.In(loc).Format("02/01/2006 15:04")
		}
	}
	return t.UTC().Format("02/01/2006 15:04") + " UTC"
}

// equipmentCSV: a mesma busca e a mesma ordem da página, sem páginas.
func (a *api) equipmentCSV(w http.ResponseWriter, r *http.Request) {
	q := parseEquipQuery(r.URL.Query())
	rows, err := a.store.InventoryList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar o inventário", err)
		return
	}
	found := filterEquip(buildEquip(rows, a.now()), q)
	sortEquip(found, q.Order, q.Desc)
	out := make([][]string, 0, len(found))
	for _, e := range found {
		status, ipSrc, collected := "offline", "", ""
		if e.Online {
			status = "online"
		}
		switch {
		case e.IPFromCheckin:
			ipSrc = "check-in"
		case e.IP != "":
			ipSrc = "interface"
		}
		if e.Collected != nil {
			collected = export.When(*e.Collected)
		}
		out = append(out, []string{e.ID, e.Name, status, e.Agent, e.IP, ipSrc, collected, export.When(e.LastSeen),
			e.Channel, e.OS, e.Category, e.Manufacturer, e.Model, e.Serial, strconv.Itoa(e.Ring), usersSummary(e.Users)})
	}
	name := "patchd-equipamentos-" + a.now().UTC().Format("20060102-1504") + ".csv"
	a.sendCSV(w, r, name, len(out), func(b *bytes.Buffer) error {
		return export.Table(b, []string{"maquina", "nome", "status", "agente", "ip", "ip_origem", "inventario_coletado_utc",
			"ultimo_contato_utc", "canal", "sistema", "categoria", "fabricante", "modelo", "serial", "anel", "usuarios"}, out)
	})
}
