package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// Campanha de terceiros pelo painel (Aula 14.8): o formulário de Campanhas ganha o tipo
// "instalar programa de terceiros", com as versões aprovadas e guardadas; a página do
// programa leva para ele já preenchido; a conferência mostra, anel a anel, o que cada um
// receberia agora. A criação passa pela mesma conferência da linha de comando
// (Campaigner.Ready), e o motor confere de novo a cada anel.

// campaigner: o planejador das campanhas de terceiros com o banco e a pasta deste servidor.
// Sem as regras, Ready responde o motivo (nada é criado).
func (a *api) campaigner() campaign.ThirdPartyPlanner {
	if a.tpManifest == nil {
		return noRules{}
	}
	return tpinstall.Campaigner{Store: a.store, Manifest: *a.tpManifest, Dir: a.packagesDir()}
}

type noRules struct{}

func (noRules) Ready(context.Context, string, string) (string, error) {
	return "o servidor está sem as regras de terceiros", nil
}

func (noRules) Job(context.Context, string, string, string) (*jobs.ThirdPartyParams, string, error) {
	return nil, "servidor sem as regras de terceiros", nil
}

// programChoices preenche as versões que podem virar campanha: aprovadas, com instalador
// guardado e regra com install. A escolhida (f.Program) fica marcada.
func (a *api) programChoices(r *http.Request, f *campaignForm) error {
	f.TPOn = a.tpManifest != nil
	if !f.TPOn {
		return nil
	}
	approvals, err := a.store.ThirdPartyApprovals(r.Context())
	if err != nil {
		return err
	}
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		return err
	}
	f.Programs = nil
	for _, ap := range approvals {
		rule := a.installRule(ap.AppID)
		if rule == nil || !slices.ContainsFunc(pkgs, func(p store.ThirdPartyPackage) bool { return p.AppID == ap.AppID && p.Version == ap.Version }) {
			continue
		}
		v := ap.AppID + " " + ap.Version
		f.Programs = append(f.Programs, programOption{Value: v, Label: rule.Name + " " + ap.Version, Selected: v == f.Program})
	}
	slices.SortFunc(f.Programs, func(x, y programOption) int { return strings.Compare(x.Label, y.Label) })
	return nil
}

// thirdPartyPreview: por anel escolhido, o que a campanha faria agora ("anel 0: 1 job(s);
// fora: 1 sem inventário do Windows"). As máquinas aposentadas não estão na lista.
func (a *api) thirdPartyPreview(r *http.Request, c campaign.Campaign) (map[int]string, error) {
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		return nil, err
	}
	tp := a.campaigner()
	out := map[int]string{}
	for _, ring := range c.Rings {
		created, skipped := 0, map[string]int{}
		for _, m := range machines {
			if m.Ring != ring {
				continue
			}
			p, skip, err := tp.Job(r.Context(), m.ID, c.App, c.Version)
			if err != nil {
				return nil, err
			}
			if p == nil {
				skipped[skip]++
				continue
			}
			created++
		}
		out[ring] = strings.TrimPrefix(campaign.RingNote(ring, created, skipped), "anel "+strconv.Itoa(ring)+": ")
	}
	return out, nil
}
