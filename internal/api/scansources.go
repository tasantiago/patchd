package api

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wuerr"
)

// Fontes de busca falhando (Aula 16.2). No lab, o WSUS recusou toda busca de uma VM por
// uma semana (0x80244007) e nada no painel mostrou: a busca chegava, com o erro guardado
// dentro dela, e a conformidade do Windows (que vem do MSRC pelo build) seguia normal. Agora
// a máquina mostra o aviso em todas as abas, e a visão geral lista quem está assim.

// maxOverviewSources: as máquinas que a visão geral lista; as outras viram uma contagem.
const maxOverviewSources = 5

type sourceAlert struct {
	MachineID, Name string
	Level, Text     string
	since           time.Time // a última resposta (zero: nenhuma guardada), para ordenar
}

// sourceLabel: a fonte para pessoas.
func sourceLabel(src string) string {
	switch src {
	case "wua-default":
		return "Windows Update da política (WSUS ou Microsoft)"
	case "wua-offline":
		return "catálogo offline (wsusscn2)"
	case "softwareupdate":
		return "softwareupdate (Apple)"
	}
	return src
}

// sourceAlerts monta os avisos. A fonte da política, o apt, o dnf e o softwareupdate são
// erro (sem eles, a máquina não recebe nada); o catálogo offline é atenção (ele só confere).
func (a *api) sourceAlerts(list []store.SourceFailure, now time.Time) []sourceAlert {
	out := make([]sourceAlert, 0, len(list))
	for _, f := range list {
		al := sourceAlert{MachineID: f.MachineID, Name: dash(f.Hostname), Level: "erro"}
		if f.Source == "wua-offline" {
			al.Level = "atencao"
		}
		when := "nenhuma busca guardada com resposta dela"
		if f.LastOK != nil {
			al.since = *f.LastOK
			when = fmt.Sprintf("última resposta em %s (%s)", a.stamp(*f.LastOK), ago(now.Sub(*f.LastOK)))
		}
		msg := f.Error
		if msg == "" {
			msg = "sem detalhe"
		}
		al.Text = fmt.Sprintf("A busca no %s falha: %s. Erro na busca de %s: %s", sourceLabel(f.Source), when, a.stamp(f.ScannedAt),
			wuerr.Annotate(msg))
		out = append(out, al)
	}
	// O mais grave primeiro: erro antes de atenção; depois quem está sem resposta há mais tempo.
	slices.SortStableFunc(out, func(x, y sourceAlert) int {
		if x.Level != y.Level {
			return cmp.Compare(levelRank(x.Level), levelRank(y.Level))
		}
		return x.since.Compare(y.since)
	})
	return out
}

func levelRank(l string) int {
	if l == "erro" {
		return 0
	}
	return 1
}

// machineSourceAlerts: os avisos de uma máquina, para a página dela.
func (a *api) machineSourceAlerts(r *http.Request, id string) ([]sourceAlert, error) {
	list, err := a.store.SourceFailures(r.Context(), id)
	if err != nil {
		return nil, err
	}
	return a.sourceAlerts(list, a.now()), nil
}

// overviewSourceAttention: os avisos da visão geral, um por máquina e fonte, até o limite.
func (a *api) overviewSourceAttention(list []store.SourceFailure, now time.Time) []ovAttention {
	alerts := a.sourceAlerts(list, now)
	var out []ovAttention
	for i, al := range alerts {
		if i == maxOverviewSources {
			out = append(out, ovAttention{Level: "atencao", Href: "/painel/",
				Text: fmt.Sprintf("… e mais %d fonte(s) de busca falhando em outras máquinas", len(alerts)-maxOverviewSources)})
			break
		}
		out = append(out, ovAttention{Level: al.Level, Href: "/painel/maquinas/" + al.MachineID, Text: al.Name + ": " + al.Text})
	}
	return out
}
