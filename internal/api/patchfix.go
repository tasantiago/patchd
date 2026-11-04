package api

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Patch › Sistema por correção (pedido de 04/11/2026): a mesma avaliação da lista por item,
// juntada pela correção que resolve. No Windows, cada KB (o build que corrige várias CVEs de
// uma vez); no macOS, a versão; no Linux, cada pacote fonte na versão que corrige (é a mesma
// linha da lista por item). Aprovar uma correção aprova todas as CVEs dela.

var kbOfFix = regexp.MustCompile(`KB[0-9]{4,8}`)

// itemOS: o sistema do item, pelo tipo e pela correção.
func itemOS(p patchItem) string {
	switch {
	case p.Kind == "Pacote":
		return "linux"
	case kbOfFix.MatchString(p.FixedIn):
		return "windows"
	case p.Kind == "CVE":
		return "macos"
	}
	return ""
}

// fixKey: a chave da correção do item (a aprovação continua por item e correção).
func fixKey(p patchItem) string {
	if p.Kind == "Pacote" || p.FixedIn == "" {
		return store.ApprovalKey(p.ID, p.FixedIn)
	}
	return p.FixedIn
}

// patchFix: uma correção, com os itens que ela resolve.
type patchFix struct {
	Key, Label, Detail, OS, Severity string
	Exploited                        bool
	Items                            []patchItem
	Machines                         map[string]bool
	Pending                          int
}

// groupFixes junta os itens por correção, na ordem da lista (exploradas e severidade primeiro).
func groupFixes(items []patchItem) []patchFix {
	byKey := map[string]*patchFix{}
	var order []string
	for _, p := range items {
		k := fixKey(p)
		f := byKey[k]
		if f == nil {
			f = &patchFix{Key: k, OS: itemOS(p), Machines: map[string]bool{}}
			switch f.OS {
			case "windows":
				f.Label, f.Detail = kbOfFix.FindString(p.FixedIn), p.FixedIn
			case "linux":
				f.Label, f.Detail = p.ID, p.FixedIn
			default:
				f.Label, f.Detail = "macOS "+p.FixedIn, ""
			}
			byKey[k] = f
			order = append(order, k)
		}
		f.Items = append(f.Items, p)
		f.Exploited = f.Exploited || p.Exploited
		if severityRank(p.Severity) > severityRank(f.Severity) {
			f.Severity = p.Severity
		}
		for _, m := range p.Machines {
			f.Machines[m.ID] = true
		}
		if p.Approval == nil {
			f.Pending++
		}
	}
	out := make([]patchFix, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// fixSel: o valor da correção no formulário do lote.
func fixSel(key string) string { return url.Values{"corr": {key}}.Encode() }

// parseFixSel desfaz fixSel.
func parseFixSel(v string) (string, bool) {
	q, err := url.ParseQuery(v)
	if err != nil || len(q["corr"]) != 1 || q.Get("corr") == "" || len(q.Get("corr")) > 2*maxApproveLen {
		return "", false
	}
	return q.Get("corr"), true
}

// fixRow: uma linha da lista por correção.
type fixRow struct {
	Label, Detail, System, Severity, Approval, ApprovalClass, Href, Sel string
	Exploited                                                           bool
	Items, Machines                                                     int
	Unit                                                                string
}

var osLabels = map[string]string{"windows": "Windows", "linux": "Linux", "macos": "macOS"}

func (f patchFix) row() fixRow {
	r := fixRow{Label: f.Label, Detail: f.Detail, System: dash(osLabels[f.OS]), Severity: dash(f.Severity), Exploited: f.Exploited,
		Items: len(f.Items), Machines: len(f.Machines), Unit: "CVE(s)"}
	if f.OS == "linux" {
		r.Unit = "pacote"
	}
	switch {
	case f.Pending == 0:
		r.Approval, r.ApprovalClass = "aprovada", "em_dia"
	case f.Pending < len(f.Items):
		r.Approval, r.ApprovalClass = fmt.Sprintf("%d de %d aprovados", len(f.Items)-f.Pending, len(f.Items)), "faltando"
	default:
		r.Approval, r.ApprovalClass = "aguardando", "reboot_pendente"
	}
	if f.Pending > 0 {
		r.Sel = fixSel(f.Key)
	}
	// A correção na lista por item: as CVEs (ou o pacote) dela.
	r.Href = "/painel/patch?" + url.Values{"q": {strings.TrimPrefix(f.Label, "macOS ")}}.Encode()
	return r
}

// patchFixView monta a lista por correção (a mesma página, com outras linhas).
func (a *api) patchFixView(w http.ResponseWriter, r *http.Request, v *patchView, q patchQuery, found []patchItem) {
	fixes := groupFixes(found)
	for _, f := range fixes {
		if f.Pending > 0 {
			v.FilterWaiting++
		}
	}
	v.CanBatch = v.Admin && v.FilterWaiting > 0
	pages := max(1, (len(fixes)+q.Per-1)/q.Per)
	q.Page = min(q.Page, pages)
	v.Page = q.Page
	from, to := (q.Page-1)*q.Per, min(q.Page*q.Per, len(fixes))
	if len(fixes) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d correções", from+1, to, len(fixes))
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
	for _, f := range fixes[from:to] {
		v.FixRows = append(v.FixRows, f.row())
	}
	a.render(w, r, http.StatusOK, "patch", v)
}

// hiddenQuery: o filtro da lista (menos status e q, que o formulário já leva) para os campos
// escondidos da conferência.
func hiddenQuery(q patchQuery) []optView {
	var out []optView
	for k, v := range map[string]string{"ver": q.View, "sistema": q.OS, "severidade": q.Sev} {
		if v != "" {
			out = append(out, optView{Value: k, Label: v})
		}
	}
	if q.Per != patchPerPage {
		out = append(out, optView{Value: "por", Label: strconv.Itoa(q.Per)})
	}
	slices.SortFunc(out, func(a, b optView) int { return strings.Compare(a.Value, b.Value) })
	return out
}

// approveFixBatch: o lote por correção. Mesmas duas etapas do lote por item: a conferência
// mostra as correções, e só uma lista fechada que passou por ela é aprovada (cada item
// aguardando de cada correção, conferido de novo no clique).
func (a *api) approveFixBatch(w http.ResponseWriter, r *http.Request, s panel.Session, items []patchItem, pq patchQuery, sels []string, byFilter bool) {
	var chosen []patchFix
	skipped := 0
	if byFilter {
		for _, f := range groupFixes(filterPatch(items, pq)) {
			if f.Pending > 0 {
				chosen = append(chosen, f)
			}
		}
	} else {
		want := map[string]bool{}
		for _, v := range sels {
			if k, ok := parseFixSel(v); ok {
				want[k] = true
			}
		}
		for _, f := range groupFixes(items) {
			if !want[f.Key] {
				continue
			}
			delete(want, f.Key)
			if f.Pending == 0 {
				skipped++ // aprovada inteira por outra pessoa depois que a lista abriu
				continue
			}
			chosen = append(chosen, f)
		}
		skipped += len(want)
	}
	back := pq.href()
	if len(chosen) == 0 {
		redirectPatch(w, r, withParams(back, "erro", "lote-vazio"))
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if r.PostFormValue("confirmar") != "1" || byFilter || utf8.RuneCountInString(note) > maxNoteLen {
		v := batchView{pageBase: a.baseFor(r.Context(), s, "Aprovar em lote", "patch"), Max: maxApproveBatch, MaxNote: maxNoteLen,
			Note: note, Status: pq.Status, Q: pq.Q, Back: back, Skipped: skipped, FixMode: true, Hidden: hiddenQuery(pq)}
		if utf8.RuneCountInString(note) > maxNoteLen && r.PostFormValue("confirmar") == "1" {
			v.Error = fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen)
		}
		if len(chosen) > maxApproveBatch {
			v.FilterTotal = len(chosen)
			chosen = chosen[:maxApproveBatch]
		}
		for _, f := range chosen {
			if f.Exploited {
				v.Exploited++
			}
			v.FixItems += f.Pending
			fr := f.row()
			v.Rows = append(v.Rows, batchRow{ID: f.Label, FixedIn: dash(f.Detail), Kind: fmt.Sprintf("%s · %d %s", fr.System, f.Pending, fr.Unit),
				Severity: dash(f.Severity), Sel: fixSel(f.Key), Exploited: f.Exploited, Machines: len(f.Machines)})
		}
		a.render(w, r, http.StatusOK, "patch-lote", v)
		return
	}
	if len(chosen) > maxApproveBatch {
		chosen = chosen[:maxApproveBatch]
	}
	var list []store.PatchApproval
	var labels []string
	for _, f := range chosen {
		labels = append(labels, f.Label)
		for _, p := range f.Items {
			if p.Approval == nil {
				list = append(list, store.PatchApproval{Item: p.ID, FixedIn: p.FixedIn, ApprovedBy: s.Username, Note: note})
			}
		}
	}
	created, err := a.store.ApprovePatches(r.Context(), list)
	if err != nil {
		a.internalError(w, r, "aprovar o lote", err)
		return
	}
	already := len(list) - len(created)
	rid := RequestID(r.Context())
	a.logger.Info("lote de aprovação por correção", "correcoes", len(chosen), "aprovados", len(created), "ja_aprovados", already,
		"fora", skipped, "username", s.Username, "request_id", rid)
	shown := labels
	if len(shown) > 20 {
		shown = append(slices.Clone(shown[:20]), fmt.Sprintf("e mais %d", len(labels)-20))
	}
	a.audit(r, s, "patch.aprovar_lote", "", fmt.Sprintf("%d correção(ões) (%s): %d item(ns) aprovado(s), %d já aprovado(s), %d fora",
		len(chosen), strings.Join(shown, ", "), len(created), already, skipped))
	redirectPatch(w, r, withParams(back, "lote", strconv.Itoa(len(created)), "ja", strconv.Itoa(already), "fora", strconv.Itoa(skipped)))
}
