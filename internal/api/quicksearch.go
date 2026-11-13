package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tasantiago/patchd/internal/texto"
)

// A busca rápida do topo (em todas as telas): o nome, o IP, o usuário conectado, o número de
// série, o tombo ou o começo do ID de uma máquina. Um resultado só abre a máquina direto;
// vários (ou nenhum) caem na lista de equipamentos já filtrada, onde dá para refinar.

// quickSearch: GET /painel/buscar?q=.
func (a *api) quickSearch(w http.ResponseWriter, r *http.Request) {
	q := texto.Clip(strings.TrimSpace(r.URL.Query().Get("q")), maxSearchLen)
	if q == "" {
		http.Redirect(w, r, "/painel/inventario", http.StatusFound)
		return
	}
	list := "/painel/inventario?" + url.Values{"q": {q}}.Encode()
	rows, err := a.store.InventoryList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar o inventário", err)
		return
	}
	all := buildEquip(rows, a.now())
	if assets, err := a.assetMap(r.Context()); err == nil {
		withAssets(all, assets)
	}
	// O começo do ID (o que as tabelas mostram, como "60ef97e4") também acha a máquina.
	needle := strings.ToLower(q)
	var byID []equip
	if len(needle) >= 4 {
		for _, e := range all {
			if strings.HasPrefix(strings.ToLower(e.ID), needle) {
				byID = append(byID, e)
			}
		}
	}
	// O nome inteiro vence o pedaço: "PC-01" abre a PC-01 mesmo existindo a PC-010.
	var exact []equip
	for _, e := range all {
		if strings.EqualFold(e.Name, q) {
			exact = append(exact, e)
		}
	}
	found := filterEquip(all, equipQuery{Field: "todos", Q: q})
	switch {
	case len(byID) == 1:
		http.Redirect(w, r, machinePath(byID[0].ID), http.StatusFound)
	case len(exact) == 1:
		http.Redirect(w, r, machinePath(exact[0].ID), http.StatusFound)
	case len(found) == 1:
		http.Redirect(w, r, machinePath(found[0].ID), http.StatusFound)
	default:
		http.Redirect(w, r, list, http.StatusFound)
	}
}
