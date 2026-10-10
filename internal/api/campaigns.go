package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/campaign"
)

// A tela das campanhas (Aula 8.5): só leitura. Criar, pausar, retomar e cancelar ficam na
// linha de comando por enquanto; o andamento é do servidor.

const campaignsShown = 50

type campaignRow struct {
	ID                                                   int64
	Name, Type, Sources, Rings, State, Label, Detail, By string
	Created                                              string
}

type campaignsView struct {
	pageBase
	Rows []campaignRow
}

func campaignLabel(s string) string {
	switch s {
	case campaign.StateRunning:
		return "em andamento"
	case campaign.StateDone:
		return "concluída"
	}
	return s
}

func (a *api) campaignsPage(w http.ResponseWriter, r *http.Request) {
	v := campaignsView{pageBase: a.base(r, "Campanhas", "campanhas")}
	list, err := a.store.Campaigns(r.Context(), campaignsShown)
	if err != nil {
		a.internalError(w, r, "listar campanhas", err)
		return
	}
	for _, c := range list {
		rings := make([]string, len(c.Rings))
		for i, x := range c.Rings {
			rings[i] = strconv.Itoa(x)
			if i == c.CurrentIdx && c.State != campaign.StateDone {
				rings[i] = "[" + rings[i] + "]"
			}
		}
		typ := typeLabel(c.Type)
		if c.UseWindow {
			typ += ", na janela"
		}
		v.Rows = append(v.Rows, campaignRow{ID: c.ID, Name: c.Name, Type: typ, Sources: dash(strings.Join(c.Sources, ", ")),
			Rings: strings.Join(rings, " → "), State: c.State, Label: campaignLabel(c.State), Detail: dash(c.Detail),
			By: c.CreatedBy, Created: ago(a.now().Sub(c.CreatedAt))})
	}
	a.render(w, r, http.StatusOK, "campanhas", v)
}
