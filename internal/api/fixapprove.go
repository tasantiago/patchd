package api

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Aprovar a correção inteira (Aula 15.9). No Windows e no macOS, o item é a CVE e a
// correção é o build ou KB: uma atualização acumulada fecha centenas de CVEs, e aprovar em
// lotes de 200 era repetir a mesma decisão cinco vezes. Aqui a decisão é uma: "aprovo o
// build X". A conferência mostra o resumo (quantos itens, quantos explorados, as
// severidades, as máquinas) e uma amostra, e leva uma impressão digital da lista; no
// clique, o servidor refaz a lista e só aprova se ela for a mesma que foi conferida.
//
// Só CVEs: no Linux, cada pacote tem a sua versão corrigida, e dois pacotes sem relação
// podem ter o mesmo texto de versão. Para eles, a aprovação continua por item ou em lote.

const (
	maxFixItems   = 5000 // uma correção com mais que isso não é uma atualização do sistema
	fixSampleRows = 20
	maxFixBody    = 16 << 10
)

// waitingOfFix: as CVEs aguardando aprovação que essa correção fecha, na ordem da lista.
func waitingOfFix(items []patchItem, fix string) []patchItem {
	var out []patchItem
	for _, p := range items {
		if p.Approval == nil && p.Kind == "CVE" && p.FixedIn == fix {
			out = append(out, p)
		}
	}
	return out
}

// fixDigest: a impressão digital da lista conferida (as chaves em ordem, SHA-256).
func fixDigest(items []patchItem) string {
	keys := make([]string, len(items))
	for i, p := range items {
		keys[i] = store.ApprovalKey(p.ID, p.FixedIn)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

type fixCount struct {
	Label string
	N     int
}

type fixView struct {
	pageBase
	FixedIn, Digest, Note, Back string
	Items, Exploited, Machines  int
	Systems                     string
	Severities                  []fixCount
	Sample                      []batchRow
	More, MaxNote               int
}

// approveFixAction: POST /painel/patch/correcao. Sem confirmar=1, a conferência; com
// confirmar=1 e a impressão digital da conferência, aprova tudo numa transação.
func (a *api) approveFixAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFixBody)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/painel/patch?erro=correcao", http.StatusSeeOther)
		return
	}
	fix := strings.TrimSpace(r.PostFormValue("fix"))
	back := (patchQuery{Status: "aguardando", Q: fix}).href()
	if fix == "" || len(fix) > maxApproveLen {
		http.Redirect(w, r, "/painel/patch?erro=correcao", http.StatusSeeOther)
		return
	}
	if a.compliance == nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
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
	chosen := waitingOfFix(items, fix)
	switch {
	case len(chosen) == 0:
		http.Redirect(w, r, withParams(back, "erro", "lote-vazio"), http.StatusSeeOther)
		return
	case len(chosen) > maxFixItems:
		http.Redirect(w, r, withParams(back, "erro", "correcao-grande"), http.StatusSeeOther)
		return
	}
	digest := fixDigest(chosen)
	note := strings.TrimSpace(r.PostFormValue("nota"))
	switch {
	case r.PostFormValue("confirmar") != "1":
		a.fixConfirm(w, r, s, fix, chosen, digest, "", "")
		return
	case utf8.RuneCountInString(note) > maxNoteLen:
		a.fixConfirm(w, r, s, fix, chosen, digest, note, fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen))
		return
	case r.PostFormValue("conferido") != digest:
		// Uma avaliação nova entre a conferência e o clique: a lista não é mais a que se viu.
		a.fixConfirm(w, r, s, fix, chosen, digest, note, "A lista desta correção mudou desde a conferência (chegou uma avaliação nova). Confira de novo antes de aprovar.")
		return
	}
	list := make([]store.PatchApproval, 0, len(chosen))
	exploited := 0
	for _, p := range chosen {
		list = append(list, store.PatchApproval{Item: p.ID, FixedIn: p.FixedIn, ApprovedBy: s.Username, Note: note})
		if p.Exploited {
			exploited++
		}
	}
	created, err := a.store.ApprovePatches(r.Context(), list)
	if err != nil {
		a.internalError(w, r, "aprovar a correção", err)
		return
	}
	already := len(list) - len(created)
	// Uma linha para a decisão: cada item aprovado fica no banco, com quem e a observação.
	a.logger.Info("correção aprovada pelo painel", "fixed_in", fix, "aprovados", len(created), "ja_aprovados", already,
		"exploradas", exploited, "conferencia", digest, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "patch.aprovar_correcao", fix, fmt.Sprintf("%d aprovada(s), %d já aprovada(s), %d explorada(s) · conferência %s", len(created), already, exploited, digest))
	http.Redirect(w, r, withParams((patchQuery{Status: "aprovados", Q: fix}).href(), "lote", strconv.Itoa(len(created)),
		"ja", strconv.Itoa(already)), http.StatusSeeOther)
}

// fixConfirm: o resumo da correção e uma amostra das CVEs, as exploradas primeiro.
func (a *api) fixConfirm(w http.ResponseWriter, r *http.Request, s panel.Session, fix string, chosen []patchItem, digest, note, errMsg string) {
	v := fixView{pageBase: a.baseFor(r.Context(), s, "Aprovar a correção", "patch"), FixedIn: fix, Digest: digest, Note: note,
		Back: (patchQuery{Status: "aguardando", Q: fix}).href(), Items: len(chosen), MaxNote: maxNoteLen}
	v.Error = errMsg
	sev := map[string]int{}
	machines, systems := map[string]bool{}, map[string]bool{}
	for _, p := range chosen {
		if p.Exploited {
			v.Exploited++
		}
		sev[cmp.Or(p.Severity, "sem severidade")]++
		for _, m := range p.Machines {
			machines[m.ID] = true
			systems[osFamily(m.OS)] = true
		}
	}
	v.Machines = len(machines)
	v.Systems = strings.Join(slices.Sorted(maps.Keys(systems)), ", ")
	for _, k := range slices.SortedFunc(maps.Keys(sev), func(x, y string) int {
		return cmp.Or(cmp.Compare(severityRank(y), severityRank(x)), strings.Compare(x, y))
	}) {
		v.Severities = append(v.Severities, fixCount{Label: k, N: sev[k]})
	}
	sample := chosen
	if len(sample) > fixSampleRows {
		v.More = len(sample) - fixSampleRows
		sample = sample[:fixSampleRows]
	}
	for _, p := range sample {
		v.Sample = append(v.Sample, batchRow{ID: p.ID, FixedIn: p.FixedIn, Kind: p.Kind, Severity: dash(p.Severity),
			Exploited: p.Exploited, Machines: len(p.Machines)})
	}
	a.render(w, r, http.StatusOK, "patch-correcao", v)
}

// singleFix: o filtro só tem CVEs aguardando de uma correção? Então a lista oferece aprovar
// a correção inteira.
func singleFix(found []patchItem) (string, int) {
	fix, n := "", 0
	for _, p := range found {
		if p.Approval != nil {
			continue
		}
		if p.Kind != "CVE" || p.FixedIn == "" || (fix != "" && p.FixedIn != fix) {
			return "", 0
		}
		fix = p.FixedIn
		n++
	}
	return fix, n
}
