package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Patch › Pendentes (Aula 13.1): a frota vista por item pendente, como o "Missing
// Patches" do ITOM. Cada linha é um item (pacote fonte do Linux, ou CVE do Windows e do
// macOS) com a correção que o resolve, e quantos sistemas ele afeta. Aprovar é por item E
// correção: a próxima correção do mesmo pacote volta a "aguardando".
//
// A aprovação só registra (quem, quando, por quê). A Aula 13.2 faz a aprovação mandar:
// atualização e campanha só com item aprovado.

const (
	patchPerPage  = 25
	maxApproveLen = 200 // item e correção: o mesmo limite do banco
	maxNoteLen    = 500
)

// patchMachine: uma máquina afetada por um item.
type patchMachine struct {
	ID, Name, OS, Catalog, Installed string
	Exploited                        bool
}

// patchItem: um item pendente na frota, com as máquinas que ele afeta.
type patchItem struct {
	ID, FixedIn, Kind, Severity, Note string
	Exploited                         bool
	Machines                          []patchMachine
	Catalogs                          []string
	Approval                          *store.PatchApproval
}

// severityRank ordena as severidades das fontes, que não usam os mesmos nomes (USN: high,
// MSRC: Critical/Important, Fedora: Moderate...).
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical", "crítica", "urgent":
		return 4
	case "high", "important", "alta":
		return 3
	case "medium", "moderate", "média":
		return 2
	case "low", "baixa", "negligible":
		return 1
	}
	return 0
}

func kindOf(unit string) string {
	switch unit {
	case "pacotes":
		return "Pacote"
	case "CVEs":
		return "CVE"
	}
	return ""
}

// aggregateItems junta as pendências da frota por item e correção. Ordem: com exploração
// conhecida primeiro, depois a severidade, depois quantos sistemas, depois o nome.
func aggregateItems(list []protocol.MachineItems, approvals []store.PatchApproval) []patchItem {
	approved := make(map[string]store.PatchApproval, len(approvals))
	for _, a := range approvals {
		approved[store.ApprovalKey(a.Item, a.FixedIn)] = a
	}
	byKey := map[string]*patchItem{}
	var order []string
	for _, m := range list {
		for _, it := range m.Items {
			k := store.ApprovalKey(it.ID, it.FixedIn)
			p := byKey[k]
			if p == nil {
				p = &patchItem{ID: it.ID, FixedIn: it.FixedIn, Kind: kindOf(m.Unit), Note: it.Note}
				if a, ok := approved[k]; ok {
					p.Approval = &a
				}
				byKey[k] = p
				order = append(order, k)
			}
			if severityRank(it.Severity) > severityRank(p.Severity) {
				p.Severity = it.Severity
			}
			p.Exploited = p.Exploited || it.Exploited
			p.Machines = append(p.Machines, patchMachine{ID: m.MachineID, Name: m.Hostname, OS: m.OS, Catalog: m.Catalog,
				Installed: it.Installed, Exploited: it.Exploited})
			if m.Catalog != "" && !slices.Contains(p.Catalogs, m.Catalog) {
				p.Catalogs = append(p.Catalogs, m.Catalog)
			}
		}
	}
	out := make([]patchItem, 0, len(order))
	for _, k := range order {
		p := byKey[k]
		slices.Sort(p.Catalogs)
		slices.SortFunc(p.Machines, func(a, b patchMachine) int {
			return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
		})
		out = append(out, *p)
	}
	slices.SortStableFunc(out, func(a, b patchItem) int {
		return cmp.Or(
			cmp.Compare(btoi(b.Exploited), btoi(a.Exploited)),
			cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)),
			cmp.Compare(len(b.Machines), len(a.Machines)),
			cmp.Compare(a.ID, b.ID),
			cmp.Compare(a.FixedIn, b.FixedIn))
	})
	return out
}

// fleetItems avalia a frota e junta por item, com as aprovações.
func (a *api) fleetItems(ctx context.Context) ([]patchItem, error) {
	ctx, cancel := context.WithTimeout(ctx, complianceTimeout)
	defer cancel()
	list, err := a.compliance.Items(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := a.store.PatchApprovals(ctx)
	if err != nil {
		return nil, err
	}
	return aggregateItems(list, approvals), nil
}

func patchItemPath(id, fixed string) string {
	return "/painel/patch/item?" + url.Values{"id": {id}, "fix": {fixed}}.Encode()
}

// --- Lista --------------------------------------------------------------------------

type patchRow struct {
	ID, FixedIn, Kind, Severity, Note, Catalogs, Href string
	Exploited                                         bool
	Machines                                          int
	ApprovedBy, ApprovedAt                            string
	Sel                                               string // valor do item no lote; vazio se já aprovado (Aula 13.4)
}

type patchView struct {
	pageBase
	Unavailable                                string
	Total, Exploited, Waiting, Approved, Hosts int
	Status, Q                                  string
	KPIAll, KPIExpl, KPIWait, KPIApproved      string
	Rows                                       []patchRow
	Shown, Prev, Next                          string
	HasFilter                                  bool
	// Lote (Aula 13.4): admin marca na lista, ou leva todos os aguardando do filtro.
	CanBatch       bool
	FilterWaiting  int
	Page, MaxBatch int
	// Aula 15.9: o filtro é uma correção só (CVEs de um build): aprovar inteira.
	WholeFix  string
	WholeFixN int
}

// patchQuery: o que a URL pede, conferido.
type patchQuery struct {
	Status, Q string
	Page      int
}

func parsePatchQuery(v url.Values) patchQuery {
	q := patchQuery{Page: 1, Q: strings.TrimSpace(v.Get("q"))}
	if len(q.Q) > maxSearchLen {
		q.Q = q.Q[:maxSearchLen]
	}
	if s := v.Get("status"); s == "aguardando" || s == "aprovados" || s == "exploradas" {
		q.Status = s
	}
	if n, _ := strconv.Atoi(v.Get("pagina")); n > 1 {
		q.Page = n
	}
	return q
}

func (q patchQuery) href() string {
	v := url.Values{}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if q.Page > 1 {
		v.Set("pagina", strconv.Itoa(q.Page))
	}
	if len(v) == 0 {
		return "/painel/patch"
	}
	return "/painel/patch?" + v.Encode()
}

func filterPatch(items []patchItem, q patchQuery) []patchItem {
	needle := strings.ToLower(q.Q)
	out := []patchItem{}
	for _, p := range items {
		switch q.Status {
		case "aguardando":
			if p.Approval != nil {
				continue
			}
		case "aprovados":
			if p.Approval == nil {
				continue
			}
		case "exploradas":
			if !p.Exploited {
				continue
			}
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.ID+" "+p.FixedIn+" "+p.Note), needle) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (a *api) patchPage(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("aba") {
	case "terceiros":
		a.thirdPartyTab(w, r)
		return
	case "geral":
		a.patchOverview(w, r)
		return
	}
	q := parsePatchQuery(r.URL.Query())
	v := patchView{pageBase: a.base(r, "Patch", "patch"), Status: q.Status, Q: q.Q, HasFilter: q.Status != "" || q.Q != "",
		MaxBatch: maxApproveBatch}
	v.Flash, v.Error = batchFlash(r.URL.Query())
	if a.compliance == nil {
		v.Unavailable = "Os itens pendentes exigem o PostgreSQL com o catálogo."
		a.render(w, r, http.StatusOK, "patch", v)
		return
	}
	items, err := a.fleetItems(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os itens pendentes", err)
		return
	}
	hosts := map[string]bool{}
	for _, p := range items {
		v.Total++
		if p.Exploited {
			v.Exploited++
		}
		if p.Approval != nil {
			v.Approved++
		} else {
			v.Waiting++
		}
		for _, m := range p.Machines {
			hosts[m.ID] = true
		}
	}
	v.Hosts = len(hosts)
	kpi := func(status string) string {
		k := q
		k.Status, k.Page = status, 1
		return k.href()
	}
	v.KPIAll, v.KPIExpl, v.KPIWait, v.KPIApproved = kpi(""), kpi("exploradas"), kpi("aguardando"), kpi("aprovados")

	found := filterPatch(items, q)
	for _, p := range found {
		if p.Approval == nil {
			v.FilterWaiting++
		}
	}
	v.CanBatch = v.Admin && v.FilterWaiting > 0
	if v.Admin && q.Q != "" {
		v.WholeFix, v.WholeFixN = singleFix(found)
	}
	pages := max(1, (len(found)+patchPerPage-1)/patchPerPage)
	q.Page = min(q.Page, pages)
	v.Page = q.Page
	from, to := (q.Page-1)*patchPerPage, min(q.Page*patchPerPage, len(found))
	if len(found) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(found))
	}
	if q.Page > 1 {
		p := q
		p.Page--
		v.Prev = p.href()
	}
	if q.Page < pages {
		p := q
		p.Page++
		v.Next = p.href()
	}
	for _, p := range found[from:to] {
		row := patchRow{ID: p.ID, FixedIn: dash(p.FixedIn), Kind: dash(p.Kind), Severity: dash(p.Severity), Note: p.Note,
			Catalogs: dash(strings.Join(p.Catalogs, ", ")), Exploited: p.Exploited, Machines: len(p.Machines),
			Href: patchItemPath(p.ID, p.FixedIn)}
		if p.Approval != nil {
			row.ApprovedBy, row.ApprovedAt = p.Approval.ApprovedBy, a.stamp(p.Approval.ApprovedAt)
		} else {
			row.Sel = batchSel(p.ID, p.FixedIn)
		}
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "patch", v)
}

// --- Item ---------------------------------------------------------------------------

type patchItemView struct {
	pageBase
	Item                  patchItem
	Pending               bool // ainda pendente em alguma máquina
	ApprovedAt            string
	CanApprove, CanRevoke bool
	MaxNote               int
}

func (a *api) patchItemPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, fixed := q.Get("id"), q.Get("fix")
	v := patchItemView{pageBase: a.base(r, "Item pendente", "patch"), MaxNote: maxNoteLen}
	switch {
	case q.Has("aprovado"):
		v.Flash = "Item aprovado: o registro guarda quem aprovou e quando."
	case q.Has("revogado"):
		v.Flash = "Aprovação revogada: o item volta a aguardar aprovação."
	case q.Get("erro") == "ja-aprovado":
		v.Error = "Este item, com esta correção, já estava aprovado; a aprovação original foi mantida."
	case q.Get("erro") == "nao-pendente":
		v.Error = "O item não está pendente em nenhuma máquina na avaliação de agora; nada foi aprovado."
	case q.Get("erro") == "nota":
		v.Error = fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen)
	}
	if id == "" || a.compliance == nil {
		a.render(w, r, http.StatusNotFound, "patch-nao-encontrado", v.pageBase)
		return
	}
	items, err := a.fleetItems(r.Context())
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "listar os itens pendentes", err)
		return
	}
	k := store.ApprovalKey(id, fixed)
	for _, p := range items {
		if store.ApprovalKey(p.ID, p.FixedIn) == k {
			v.Item, v.Pending = p, true
		}
	}
	if !v.Pending {
		// Não está mais pendente: mostra a aprovação, se houver, para poder revogar.
		approvals, err := a.store.PatchApprovals(r.Context())
		if err != nil {
			a.internalError(w, r, "listar aprovações", err)
			return
		}
		for _, ap := range approvals {
			if store.ApprovalKey(ap.Item, ap.FixedIn) == k {
				v.Item = patchItem{ID: ap.Item, FixedIn: ap.FixedIn, Approval: &ap}
			}
		}
		if v.Item.ID == "" {
			a.render(w, r, http.StatusNotFound, "patch-nao-encontrado", v.pageBase)
			return
		}
	}
	v.Title = v.Item.ID
	if v.Item.Approval != nil {
		v.ApprovedAt = a.stamp(v.Item.Approval.ApprovedAt)
	}
	v.CanApprove = v.Admin && v.Pending && v.Item.Approval == nil
	v.CanRevoke = v.Admin && v.Item.Approval != nil
	a.render(w, r, http.StatusOK, "patch-item", v)
}

// --- Ações --------------------------------------------------------------------------

// patchForm lê e confere o item e a correção do formulário.
func patchForm(r *http.Request) (id, fixed string, ok bool) {
	id, fixed = strings.TrimSpace(r.PostFormValue("id")), strings.TrimSpace(r.PostFormValue("fix"))
	return id, fixed, id != "" && len(id) <= maxApproveLen && len(fixed) <= maxApproveLen
}

// approvePatchAction aprova um item com a sua correção. Só o que está pendente agora em
// alguma máquina: aprovar um nome qualquer não deixa rastro útil.
func (a *api) approvePatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, fixed, ok := patchForm(r)
	if !ok || a.compliance == nil {
		http.Redirect(w, r, "/painel/patch", http.StatusSeeOther)
		return
	}
	back := patchItemPath(id, fixed)
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxNoteLen {
		http.Redirect(w, r, back+"&erro=nota", http.StatusSeeOther)
		return
	}
	items, err := a.fleetItems(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os itens pendentes", err)
		return
	}
	if !slices.ContainsFunc(items, func(p patchItem) bool { return p.ID == id && p.FixedIn == fixed }) {
		http.Redirect(w, r, back+"&erro=nao-pendente", http.StatusSeeOther)
		return
	}
	created, err := a.store.ApprovePatch(r.Context(), store.PatchApproval{Item: id, FixedIn: fixed, ApprovedBy: s.Username, Note: note})
	if err != nil {
		a.internalError(w, r, "aprovar o item", err)
		return
	}
	if !created {
		http.Redirect(w, r, back+"&erro=ja-aprovado", http.StatusSeeOther)
		return
	}
	a.logger.Info("patch aprovado", "item", id, "fixed_in", fixed, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, back+"&aprovado=1", http.StatusSeeOther)
}

// revokePatchAction revoga a aprovação; o log guarda quem revogou.
func (a *api) revokePatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, fixed, ok := patchForm(r)
	if !ok {
		http.Redirect(w, r, "/painel/patch", http.StatusSeeOther)
		return
	}
	removed, err := a.store.RevokePatch(r.Context(), id, fixed)
	if err != nil {
		a.internalError(w, r, "revogar a aprovação", err)
		return
	}
	if removed {
		a.logger.Info("aprovação de patch revogada", "item", id, "fixed_in", fixed, "username", s.Username, "request_id", RequestID(r.Context()))
	}
	http.Redirect(w, r, patchItemPath(id, fixed)+"&revogado=1", http.StatusSeeOther)
}

// --- Aprovação nas pendências da máquina (Aula 13.2) --------------------------------

// pendingRow: uma pendência no detalhe da máquina, com a aprovação.
type pendingRow struct {
	protocol.PendingItem
	Approved bool
	Href     string // o item em Patch, onde se aprova
	CanPick  bool   // pode ir num job update: Linux (tem pacotes) e aprovado
}

func pendingRows(items []protocol.PendingItem, approvals []store.PatchApproval) []pendingRow {
	ok := make(map[string]bool, len(approvals))
	for _, a := range approvals {
		ok[store.ApprovalKey(a.Item, a.FixedIn)] = true
	}
	out := make([]pendingRow, 0, len(items))
	for _, it := range items {
		approved := ok[store.ApprovalKey(it.ID, it.FixedIn)]
		out = append(out, pendingRow{PendingItem: it, Approved: approved, Href: patchItemPath(it.ID, it.FixedIn),
			CanPick: approved && len(it.Packages) > 0})
	}
	return out
}
