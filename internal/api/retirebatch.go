package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Limpeza do parque: as máquinas que pararam de falar com o servidor (a VM apagada, o registro
// que ficou de uma reinstalação, o agente que se registrou e nunca mandou o inventário).
// Primeiro, no Inventário, o filtro e "aposentar as encontradas": só as offline saem; a
// credencial delas deixa de valer e dá para restaurar. Depois, em Aposentadas, "excluir de
// vez" as marcadas.

// maxDeleteBatch: quantas aposentadas uma exclusão leva.
const maxDeleteBatch = 500

// equipRetireAction: POST /painel/inventario/aposentar. O filtro vem do formulário, como no
// lote do grupo de implantação; o servidor refaz a lista e confere a contagem que o admin viu.
func (a *api) equipRetireAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
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
	if !q.hasFilter() {
		back("erro=lote-filtro")
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("motivo"))
	if n := utf8.RuneCountInString(reason); n < minReasonLength || n > maxReasonLength {
		back("erro=motivo-lote")
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
	retired, online := 0, 0
	for _, e := range found {
		if e.Online { // a que está falando com o servidor não sai por lote
			online++
			continue
		}
		changed, err := a.store.RetireMachine(r.Context(), e.ID, reason, s.Username)
		if err != nil {
			a.internalError(w, r, "aposentar máquina", err)
			return
		}
		if changed {
			retired++
			a.audit(r, s, "maquina.aposentar", e.ID, reason+" (lote do Inventário)")
		}
	}
	a.creds.reset() // as credenciais das aposentadas deixam de valer já
	crit := describeEquipFilter(q)
	a.logger.Info("máquinas aposentadas pelo filtro do Inventário", "filtro", crit, "aposentadas", retired, "online", online,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "maquina.aposentar_lote", crit, fmt.Sprintf("%d aposentada(s), %d online ficaram; motivo: %s", retired, online, reason))
	back(fmt.Sprintf("aposentadas=%d&online=%d", retired, online))
}

// deleteRetiredAction: POST /painel/aposentadas/excluir. As marcadas, com a confirmação.
func (a *api) deleteRetiredAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	back := func(q url.Values) { http.Redirect(w, r, "/painel/aposentadas?"+q.Encode(), http.StatusSeeOther) }
	ids := r.PostForm["maquina"]
	switch {
	case len(ids) == 0:
		back(url.Values{"erro": {"nenhuma"}})
		return
	case len(ids) > maxDeleteBatch:
		back(url.Values{"erro": {"muitas"}})
		return
	case r.PostFormValue("confirmar") != "1":
		back(url.Values{"erro": {"confirmar"}})
		return
	}
	deleted := 0
	kept := map[string]int{}
	for _, id := range ids {
		if !machineIDPattern.MatchString(id) {
			continue
		}
		err := a.store.DeleteRetiredMachine(r.Context(), id)
		switch {
		case err == nil:
			deleted++
			a.audit(r, s, "maquina.excluir", id, "excluída de vez (aposentada)")
		case errors.Is(err, store.ErrKeepsBIOS):
			kept["bios"]++
		case errors.Is(err, store.ErrMerged):
			kept["mescla"]++
		case errors.Is(err, store.ErrHasTerminal):
			kept["terminal"]++
		case errors.Is(err, store.ErrNotRetired), errors.Is(err, store.ErrMachineNotFound):
			kept["outras"]++
		default:
			a.internalError(w, r, "excluir máquina", err)
			return
		}
	}
	a.listChanged()
	a.logger.Info("máquinas aposentadas excluídas", "excluidas", deleted, "ficaram", kept, "username", s.Username,
		"request_id", RequestID(r.Context()))
	q := url.Values{"excluidas": {strconv.Itoa(deleted)}}
	for k, n := range kept {
		q.Set("ficou_"+k, strconv.Itoa(n))
	}
	back(q)
}

// retiredFlash: as mensagens da página das aposentadas depois da exclusão.
func retiredFlash(q url.Values, b *pageBase) {
	switch q.Get("erro") {
	case "nenhuma":
		b.Error = "Marque as máquinas a excluir."
	case "muitas":
		b.Error = fmt.Sprintf("Uma exclusão leva até %d máquinas.", maxDeleteBatch)
	case "confirmar":
		b.Error = "Marque a confirmação: a exclusão não tem volta."
	}
	if !q.Has("excluidas") {
		return
	}
	b.Flash = fmt.Sprintf("%s máquina(s) excluída(s) de vez.", q.Get("excluidas"))
	for _, k := range []struct{ key, text string }{
		{"ficou_bios", "com a senha da BIOS guardada (o registro guarda a cópia)"},
		{"ficou_mescla", "que participam de uma mescla"},
		{"ficou_terminal", "com sessões do Terminal (auditoria)"},
		{"ficou_outras", "que não estavam aposentadas ou já não existiam"},
	} {
		if n := q.Get(k.key); n != "" && n != "0" {
			b.Flash += fmt.Sprintf(" Ficaram %s %s.", n, k.text)
		}
	}
}
