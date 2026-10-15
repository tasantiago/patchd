package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Aprovação em lote (Aula 13.4). Duas etapas, como em qualquer ação que mexe em muitos
// itens de uma vez:
//
//  1. Conferir: a lista marcada (ou o filtro inteiro) vira uma página que mostra
//     exatamente o que será aprovado. Nada é gravado.
//  2. Aprovar: o servidor confere de novo cada item (ainda pendente? já aprovado por
//     outra pessoa?) e grava o lote numa transação só.
//
// "Aprovar todos do filtro" vira uma lista fechada na conferência: o que se aprova é o
// que se viu, mesmo que uma nova avaliação traga itens novos entre um clique e outro.

const (
	maxApproveBatch = 200       // itens por lote: a página de conferência ainda é legível
	maxBatchBytes   = 256 << 10 // 200 itens com nomes longos cabem com folga
)

// batchSel é o valor de um item no formulário: id e correção juntos, como numa URL.
func batchSel(id, fixed string) string { return url.Values{"id": {id}, "fix": {fixed}}.Encode() }

// parseBatchSel desfaz batchSel; ok=false para valores fora do formato.
func parseBatchSel(v string) (id, fixed string, ok bool) {
	q, err := url.ParseQuery(v)
	if err != nil || len(q["id"]) != 1 || len(q["fix"]) != 1 {
		return "", "", false
	}
	id, fixed = q.Get("id"), q.Get("fix")
	return id, fixed, id != "" && len(id) <= maxApproveLen && len(fixed) <= maxApproveLen
}

type batchRow struct {
	ID, FixedIn, Kind, Severity, Sel string
	Exploited                        bool
	Machines                         int
}

type batchView struct {
	pageBase
	Rows            []batchRow
	Exploited       int
	FilterTotal     int // o filtro tinha mais que maxApproveBatch: quantos
	Max, MaxNote    int
	Note            string
	Status, Q, Back string
	Skipped         int // marcados que não estão mais aguardando
}

// approveBatchAction é a ação das duas etapas: sem confirmar=1, mostra a conferência;
// com confirmar=1, aprova.
func (a *api) approveBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/painel/patch?erro=lote-grande", http.StatusSeeOther)
		return
	}
	pq := parsePatchQuery(r.PostForm) // o filtro de onde o lote saiu, para voltar a ele
	back := pq.href()
	if a.compliance == nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	sels := r.PostForm["sel"]
	byFilter := r.PostFormValue("filtro") == "1"
	if !byFilter && len(sels) > maxApproveBatch {
		http.Redirect(w, r, withParams(back, "erro", "lote-grande"), http.StatusSeeOther)
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

	// Os escolhidos, na ordem da lista (exploradas e severidade primeiro).
	var chosen []patchItem
	skipped := 0
	if byFilter {
		for _, p := range filterPatch(items, pq) {
			if p.Approval == nil {
				chosen = append(chosen, p)
			}
		}
	} else {
		want := map[string]bool{}
		for _, v := range sels {
			id, fixed, ok := parseBatchSel(v)
			if ok {
				want[store.ApprovalKey(id, fixed)] = true
			}
		}
		for _, p := range items {
			k := store.ApprovalKey(p.ID, p.FixedIn)
			if !want[k] {
				continue
			}
			delete(want, k)
			if p.Approval != nil {
				skipped++ // aprovado por outra pessoa depois que a lista foi aberta
				continue
			}
			chosen = append(chosen, p)
		}
		skipped += len(want) // não estão mais pendentes em máquina nenhuma
	}

	// Só se aprova uma lista fechada, que passou pela conferência: o filtro sempre volta
	// para ela, mesmo com confirmar=1 forjado.
	if r.PostFormValue("confirmar") != "1" || byFilter {
		a.batchConfirm(w, r, s, chosen, skipped, pq, "", "")
		return
	}

	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxNoteLen {
		a.batchConfirm(w, r, s, chosen, skipped, pq, note, fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen))
		return
	}
	if len(chosen) == 0 {
		http.Redirect(w, r, withParams(back, "lote", "0", "fora", strconv.Itoa(skipped)), http.StatusSeeOther)
		return
	}
	list := make([]store.PatchApproval, 0, len(chosen))
	for _, p := range chosen {
		list = append(list, store.PatchApproval{Item: p.ID, FixedIn: p.FixedIn, ApprovedBy: s.Username, Note: note})
	}
	created, err := a.store.ApprovePatches(r.Context(), list)
	if err != nil {
		a.internalError(w, r, "aprovar o lote", err)
		return
	}
	rid := RequestID(r.Context())
	for _, ap := range created {
		a.logger.Info("patch aprovado", "item", ap.Item, "fixed_in", ap.FixedIn, "username", s.Username, "lote", true, "request_id", rid)
	}
	already := len(list) - len(created)
	a.logger.Info("lote de aprovação", "aprovados", len(created), "ja_aprovados", already, "fora", skipped,
		"username", s.Username, "request_id", rid)
	http.Redirect(w, r, withParams(back, "lote", strconv.Itoa(len(created)), "ja", strconv.Itoa(already),
		"fora", strconv.Itoa(skipped)), http.StatusSeeOther)
}

// batchConfirm mostra a conferência. Mais que maxApproveBatch: leva os primeiros, pela ordem da
// lista, e diz quantos o filtro tinha.
func (a *api) batchConfirm(w http.ResponseWriter, r *http.Request, s panel.Session, chosen []patchItem, skipped int,
	pq patchQuery, note, errMsg string) {
	if len(chosen) == 0 {
		http.Redirect(w, r, withParams(pq.href(), "erro", "lote-vazio"), http.StatusSeeOther)
		return
	}
	v := batchView{pageBase: a.baseFor(s, "Aprovar em lote", "patch"), Max: maxApproveBatch, MaxNote: maxNoteLen,
		Note: note, Status: pq.Status, Q: pq.Q, Back: pq.href(), Skipped: skipped}
	v.Error = errMsg
	if len(chosen) > maxApproveBatch {
		v.FilterTotal = len(chosen)
		chosen = chosen[:maxApproveBatch]
	}
	for _, p := range chosen {
		if p.Exploited {
			v.Exploited++
		}
		v.Rows = append(v.Rows, batchRow{ID: p.ID, FixedIn: dash(p.FixedIn), Kind: dash(p.Kind), Severity: dash(p.Severity),
			Sel: batchSel(p.ID, p.FixedIn), Exploited: p.Exploited, Machines: len(p.Machines)})
	}
	a.render(w, r, http.StatusOK, "patch-lote", v)
}

// withParams acrescenta parâmetros a um caminho que pode já ter query.
func withParams(path string, kv ...string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// batchFlash: a mensagem da lista depois de um lote (os números vêm da URL, conferidos).
func batchFlash(q url.Values) (flash, errMsg string) {
	switch q.Get("erro") {
	case "lote-vazio":
		return "", "Nenhum item aguardando aprovação foi marcado."
	case "lote-grande":
		return "", fmt.Sprintf("Um lote tem no máximo %d itens.", maxApproveBatch)
	case "correcao":
		return "", "Correção ausente ou fora do formato."
	case "correcao-grande":
		return "", fmt.Sprintf("A correção tem mais de %d itens aguardando: aprove em lotes.", maxFixItems)
	}
	if !q.Has("lote") {
		return "", ""
	}
	n := func(k string) int {
		v, err := strconv.Atoi(q.Get(k))
		if err != nil || v < 0 {
			return 0
		}
		return v
	}
	lote, ja, fora := n("lote"), n("ja"), n("fora")
	msg := fmt.Sprintf("%d %s em lote.", lote, plural(lote, "item aprovado", "itens aprovados"))
	if ja > 0 {
		msg += fmt.Sprintf(" %d já %s por outra pessoa; a aprovação original ficou.", ja, plural(ja, "estava aprovado", "estavam aprovados"))
	}
	if fora > 0 {
		msg += fmt.Sprintf(" %d %s de fora: não %s mais aguardando.", fora, plural(fora, "ficou", "ficaram"), plural(fora, "está", "estão"))
	}
	return msg, ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
