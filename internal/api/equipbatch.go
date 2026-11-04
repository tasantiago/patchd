package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
)

// O lote do Inventário: pôr todas as máquinas encontradas pelo filtro (faixa de IP, prédio,
// sistema, status...) num grupo de implantação de uma vez. O formulário leva o filtro e a
// contagem que o admin viu; o servidor refaz o filtro na hora e só move se a contagem bate
// (uma máquina que entrou ou saiu da faixa no meio não é movida sem ninguém ver).

// equipBatchAction: POST /painel/inventario/grupo.
func (a *api) equipBatchAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	q := parseEquipQuery(r.PostForm)
	q.Page, q.Per, q.Order, q.Desc = 1, defaultPerPage, "nome", false
	back := func(extra string) {
		to := q.href("/painel/inventario")
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		http.Redirect(w, r, to+sep+extra, http.StatusSeeOther)
	}
	ring, ok := formRing(r, "anel")
	if !ok {
		back("erro=anel")
		return
	}
	if !q.hasFilter() {
		back("erro=lote-filtro")
		return
	}
	want, err := strconv.Atoi(r.PostFormValue("n"))
	if err != nil || want <= 0 {
		back("erro=lote-mudou")
		return
	}
	rows, err := a.store.InventoryList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar o inventário", err)
		return
	}
	all := buildEquip(rows, a.now())
	assets, err := a.assetMap(r.Context())
	if err != nil {
		a.internalError(w, r, "ler o patrimônio", err)
		return
	}
	withAssets(all, assets)
	found := filterEquip(all, q)
	if len(found) != want {
		back(fmt.Sprintf("erro=lote-mudou&antes=%d&agora=%d", want, len(found)))
		return
	}
	moved, already, skipped := 0, 0, 0
	for _, e := range found {
		if e.Ring == ring {
			already++
			continue
		}
		changed, err := a.store.SetMachineRing(r.Context(), e.ID, ring)
		if err != nil {
			a.internalError(w, r, "mudar grupo de implantação", err)
			return
		}
		if changed {
			moved++
		} else {
			skipped++
		}
	}
	crit := describeEquipFilter(q)
	a.logger.Info("grupo de implantação alterado pelo filtro do Inventário", "ring", ring, "filtro", crit, "movidas", moved,
		"ja_estavam", already, "ignoradas", skipped, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "maquina.anel_lote", fmt.Sprintf("grupo de implantação %d", ring),
		fmt.Sprintf("pelo Inventário (%s): %d movida(s), %d já estavam, %d ignorada(s)", crit, moved, already, skipped))
	back(fmt.Sprintf("movidas=%d&ignoradas=%d&ja=%d&anel=%d", moved, skipped, already, ring))
}

// describeEquipFilter: o filtro em palavras, para a auditoria e o log.
func describeEquipFilter(q equipQuery) string {
	var parts []string
	if r, ok := q.ipRange(); ok {
		parts = append(parts, fmt.Sprintf("IP de %s a %s", r.lo, r.hi))
	} else if q.Q != "" {
		field := q.Field
		for _, f := range searchFields {
			if f.Key == q.Field {
				field = strings.ToLower(f.Label)
			}
		}
		parts = append(parts, fmt.Sprintf("%s contém %q", field, q.Q))
	}
	if q.Building != "" {
		parts = append(parts, "prédio "+q.Building)
	}
	if q.Status != "" {
		parts = append(parts, "status "+q.Status)
	}
	if q.Firmware != "" {
		parts = append(parts, "firmware "+q.Firmware)
	}
	return strings.Join(parts, "; ")
}

// equipBatchFlash: os avisos próprios do lote do Inventário (os de sucesso são os do lote da
// Parque, fleetRingFlash).
func equipBatchFlash(q url.Values, b *pageBase) {
	switch q.Get("erro") {
	case "lote-filtro":
		b.Error = "Filtre a lista antes (faixa de IP, prédio, sistema...): o lote não move o parque inteiro."
	case "lote-mudou":
		antes, _ := strconv.Atoi(q.Get("antes"))
		agora, _ := strconv.Atoi(q.Get("agora"))
		b.Error = fmt.Sprintf("A lista mudou entre a página e o pedido (eram %d, agora são %d): nada foi movido. Confira a lista e peça de novo.", antes, agora)
	}
	if q.Has("movidas") && q.Get("ja") != "" {
		if ja, _ := strconv.Atoi(q.Get("ja")); ja > 0 {
			b.Flash += fmt.Sprintf(" %d já estava(m) nesse grupo.", ja)
		}
	}
}
