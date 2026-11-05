package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// Alertas em lote: fechar (ou reconhecer) os marcados na tabela, ou todos os abertos do
// filtro, com conferência. Fechar à mão zera a contagem da regra daquela medida: se o
// problema continuar, um alerta novo abre depois dos minutos seguidos.

// maxAlertBatch: o maior lote (o parque todo, com folga: 2000 máquinas × CPU, memória e discos).
const maxAlertBatch = 20000

// alertBatchShown: quantos a conferência lista; os outros viram uma contagem.
const alertBatchShown = 50

type alertBatchView struct {
	pageBase
	Action, ActionLabel string // fechar | reconhecer
	Which               string // marcados | todos
	Metric, MetricL     string
	IDs                 []int64 // os marcados (no "todos", vazio: o servidor refaz a lista)
	Rows                []perfAlertRow
	N, More             int
	Note                string
	Digest              string
	Changed             bool // a lista mudou entre a conferência e a confirmação
	Batch               bool // a tabela da conferência, sem as caixas
	MaxNote             int
}

// alertBatchTargets: os alertas que o lote pega agora. Fechar: os abertos; reconhecer: os
// abertos ainda sem reconhecimento. Na medida do filtro; no "marcados", só os marcados.
func (a *api) alertBatchTargets(r *http.Request, action, which, metric string, marked map[int64]bool) ([]store.PerfAlert, error) {
	list, err := a.store.PerfAlerts(r.Context(), store.PerfAlertFilter{OpenOnly: true, Limit: maxAlertBatch})
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, al := range list {
		switch {
		case al.ClosedAt != nil, metric != "" && al.Metric != metric, action == "reconhecer" && al.AckAt != nil,
			which == "marcados" && !marked[al.ID]:
			continue
		}
		out = append(out, al)
	}
	return out, nil
}

func alertIDs(list []store.PerfAlert) (ids []int64, keys []string) {
	for _, al := range list {
		ids = append(ids, al.ID)
		keys = append(keys, strconv.FormatInt(al.ID, 10))
	}
	return ids, keys
}

// alertBatchAction: POST /painel/alertas/lote. Sem confirmar, a conferência; com confirmar e
// a mesma lista da conferência, aplica.
func (a *api) alertBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	action, which, metric := r.PostFormValue("acao"), r.PostFormValue("quais"), r.PostFormValue("medida")
	back := url.Values{}
	switch metric {
	case "", perfeval.MetricCPU, perfeval.MetricMem, perfeval.MetricDisk:
		if metric != "" {
			back.Set("medida", metric)
		}
	default:
		http.Error(w, "medida inválida", http.StatusBadRequest)
		return
	}
	if (action != "fechar" && action != "reconhecer") || (which != "marcados" && which != "todos") {
		http.Error(w, "pedido inválido", http.StatusBadRequest)
		return
	}
	redirect := func(k, v string) {
		back.Set(k, v)
		http.Redirect(w, r, "/painel/alertas?"+back.Encode(), http.StatusSeeOther)
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxAckNote || strings.ContainsFunc(note, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		redirect("erro", "nota")
		return
	}
	marked := map[int64]bool{}
	if which == "marcados" {
		if len(r.PostForm["alerta"]) > maxAlertBatch {
			http.Error(w, "alertas demais", http.StatusBadRequest)
			return
		}
		for _, v := range r.PostForm["alerta"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				marked[id] = true
			}
		}
	}
	list, err := a.alertBatchTargets(r, action, which, metric, marked)
	if err != nil {
		a.internalError(w, r, "listar alertas", err)
		return
	}
	if len(list) == 0 {
		redirect("erro", "lote-vazio")
		return
	}
	ids, keys := alertIDs(list)
	digest := batchDigest(keys)
	confirmed := r.PostFormValue("confirmar") == "1"
	if !confirmed || r.PostFormValue("lista") != digest {
		v := alertBatchView{pageBase: a.baseFor(r.Context(), s, "Conferir os alertas", "alertas"), Action: action, Which: which,
			Metric: metric, N: len(list), Note: note, Digest: digest, Changed: confirmed}
		v.ActionLabel = "Fechar"
		if action == "reconhecer" {
			v.ActionLabel = "Reconhecer"
		}
		if metric != "" {
			v.MetricL = metricLabel(metric, "")
		}
		if which == "marcados" {
			v.IDs = ids
		}
		names, err := a.machineNames(r.Context())
		if err != nil {
			a.internalError(w, r, "listar máquinas", err)
			return
		}
		shown := list
		if len(shown) > alertBatchShown {
			shown, v.More = shown[:alertBatchShown], len(shown)-alertBatchShown
		}
		v.Rows = a.alertRows(shown, names, false)
		a.render(w, r, http.StatusOK, "alertas-lote", v)
		return
	}
	var n int
	if action == "fechar" {
		n, err = a.store.ClosePerfAlerts(r.Context(), ids, s.Username, note, a.now())
	} else {
		n, err = a.store.AckPerfAlerts(r.Context(), ids, s.Username, note, a.now())
	}
	if err != nil {
		a.internalError(w, r, action+" alertas", err)
		return
	}
	scope := "os marcados"
	if which == "todos" {
		scope = "todos os abertos"
	}
	if metric != "" {
		scope += " de " + metricLabel(metric, "")
	}
	a.audit(r, s, "alerta."+action+"-lote", fmt.Sprintf("%d alerta(s)", n),
		texto.Clip(fmt.Sprintf("%s; nota: %s; ids: %s", scope, note, strings.Join(keys, ",")), 2000))
	if action == "fechar" {
		redirect("fechados", strconv.Itoa(n))
		return
	}
	redirect("reconhecidos", strconv.Itoa(n))
}

// numOnly: a contagem do aviso, só se for número (a URL vem do navegador).
func numOnly(s string) string {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return strconv.Itoa(n)
	}
	return "?"
}
