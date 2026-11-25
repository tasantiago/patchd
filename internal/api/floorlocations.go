package api

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// O prefixo não pode ser uma faixa válida; a chave do ranking é o prédio e o
// nome físico, de modo que duas VLANs do mesmo andar contem juntas.
const namedFloorPrefix = "andar:"

type floorLocationStore interface {
	FloorLocations(context.Context) (map[store.FloorKey]string, error)
	SaveFloorLocations(context.Context, []store.FloorLocation, string) error
}

func (a *api) floorNames(ctx context.Context) (map[store.FloorKey]string, error) {
	if db, ok := a.store.(floorLocationStore); ok {
		return db.FloorLocations(ctx)
	}
	return nil, nil
}

// floorIP conserva a regra anterior do Shield: a faixa mais específica.
func floorIP(e equip, rules []ipRule) *ipRule {
	var best *ipRule
	for i := range rules {
		r := &rules[i]
		if e.inRange(r.rng) && (best == nil || r.size < best.size || (r.size == best.size && r.Spec < best.Spec)) {
			best = r
		}
	}
	return best
}

type floorLocationRow struct {
	Spec, Name, Legacy string
	Machines int
}

type floorBuildingView struct {
	Building string
	Rows []floorLocationRow
	Machines, Named int
}

func (a *api) floorLocationsView(ctx context.Context) ([]floorBuildingView, error) {
	names, err := a.floorNames(ctx)
	if err != nil {
		return nil, err
	}
	fleet, err := a.fleetEquip(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := a.loadRingRules(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]map[string]int{}
	for _, e := range fleet {
		b := e.Asset.Building
		if b == "" {
			continue
		}
		if r := floorIP(e, rules.ips); r != nil {
			if counts[b] == nil {
				counts[b] = map[string]int{}
			}
			counts[b][r.Spec]++
		}
	}
	// Mantenha editáveis os vínculos cadastrados mesmo sem máquinas atuais.
	for k := range names {
		if counts[k.Building] == nil {
			counts[k.Building] = map[string]int{}
		}
		if _, ok := counts[k.Building][k.Spec]; !ok {
			counts[k.Building][k.Spec] = 0
		}
	}
	var legacy map[string]string
	if _, ok := a.shieldDB(); ok {
		pol, _, err := a.shieldPolicy(ctx)
		if err != nil {
			return nil, err
		}
		legacy = pol.Ranking.FloorNames
	}
	var out []floorBuildingView
	for b, specs := range counts {
		if len(specs) < 2 {
			hasSaved := false
			for spec := range specs {
				if names[store.FloorKey{Building: b, Spec: spec}] != "" {
					hasSaved = true
				}
			}
			if !hasSaved {
				continue
			}
		}
		row := floorBuildingView{Building: b}
		for spec, n := range specs {
			name := names[store.FloorKey{Building: b, Spec: spec}]
			row.Rows = append(row.Rows, floorLocationRow{Spec: spec, Name: name, Legacy: legacy[spec], Machines: n})
			row.Machines += n
			if name != "" || legacy[spec] != "" {
				row.Named++
			}
		}
		slices.SortFunc(row.Rows, func(x, y floorLocationRow) int {
			xr, xok := parseIPRange(x.Spec)
			yr, yok := parseIPRange(y.Spec)
			if xok && yok {
				if c := xr.lo.Compare(yr.lo); c != 0 {
					return c
				}
			}
			return strings.Compare(x.Spec, y.Spec)
		})
		out = append(out, row)
	}
	slices.SortFunc(out, func(x, y floorBuildingView) int { return strings.Compare(x.Building, y.Building) })
	return out, nil
}

func locationsFlash(q url.Values, v *pageBase) {
	if q.Has("gravado") {
		v.Flash = "Localizações gravadas. O Shield usa os novos nomes na próxima avaliação."
	} else if q.Has("erro") {
		v.Error = "Nada foi gravado: " + shortError(q.Get("erro"), 200)
	}
}

func (a *api) floorLocationsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q url.Values) {
		http.Redirect(w, r, "/painel/configuracao?aba=localizacoes&"+q.Encode(), http.StatusSeeOther)
	}
	fail := func(msg string) { back(url.Values{"erro": {msg}}) }
	db, ok := a.store.(floorLocationStore)
	if !ok {
		fail("armazenamento não suporta localizações")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		fail("formulário inválido")
		return
	}
	b := r.PostFormValue("predio")
	specs, names := r.PostForm["faixa"], r.PostForm["nome"]
	if b == "" || len(specs) == 0 || len(specs) != len(names) || len(specs) > 500 {
		fail("formulário inválido")
		return
	}
	view, err := a.floorLocationsView(r.Context())
	if err != nil {
		a.internalError(w, r, "ler localizações", err)
		return
	}
	allowed := map[string]bool{}
	for _, building := range view {
		if building.Building == b {
			for _, row := range building.Rows {
				allowed[row.Spec] = true
			}
		}
	}
	var list []store.FloorLocation
	var detail []string
	for i, spec := range specs {
		name := strings.TrimSpace(names[i])
		if !allowed[spec] || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			fail("faixa ou nome inválido")
			return
		}
		delete(allowed, spec)
		list = append(list, store.FloorLocation{FloorKey: store.FloorKey{Building: b, Spec: spec}, Name: name})
		detail = append(detail, fmt.Sprintf("%s=%q", spec, name))
	}
	if err := db.SaveFloorLocations(r.Context(), list, s.Username); err != nil {
		a.internalError(w, r, "gravar localizações", err)
		return
	}
	a.audit(r, s, "localizacoes.andares", b, strings.Join(detail, "; "))
	back(url.Values{"gravado": {"1"}})
}

var _ = template.Must(pages.New("localizacoes").Parse(`<div class="ux-locations"><header class="ux-hero"><div><span class="ux-eyebrow">Organização do parque</span><h2>Prédios e andares</h2><p>Associe as redes aos pavimentos físicos de cada prédio.</p></div><div class="ux-hero-side"><span class="ux-status">{{len .Locations}} prédio(s) identificado(s)</span></div></header>
<div class="locations-guide"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 7.5v.5"/></svg><div><b>Como organizar os andares</b><p>As faixas vêm das regras dos grupos de implantação e o prédio vem do patrimônio. Use o mesmo nome exato em duas ou mais faixas do mesmo prédio para representar um único andar. Deixe vazio para usar o nome anterior do Shield, se houver, ou o automático.</p></div></div>
{{if .Locations}}{{range .Locations}}<details class="lugar" open><summary><span class="location-summary"><b>{{.Building}}</b><small>{{len .Rows}} faixa(s) · {{.Machines}} computador(es) · {{.Named}} faixa(s) nomeada(s)</small></span></summary>
<form method="post" action="/painel/configuracao/localizacoes"><input type="hidden" name="predio" value="{{.Building}}">
<div class="location-table"><table><thead><tr><th scope="col">Faixa de IP</th><th scope="col">Computadores</th><th scope="col">Nome do andar</th></tr></thead><tbody>
{{range .Rows}}<tr><td><code>{{.Spec}}</code><input type="hidden" name="faixa" value="{{.Spec}}"></td><td class="num">{{.Machines}}</td><td><input aria-label="Nome do andar da faixa {{.Spec}}" name="nome" value="{{.Name}}" maxlength="40" placeholder="{{if .Legacy}}{{.Legacy}}{{else}}Ex.: Térreo, 1º andar{{end}}">{{if .Legacy}}<small class="muted">Nome anterior: {{.Legacy}}</small>{{end}}</td></tr>{{end}}
</tbody></table></div><div class="location-footer"><small>Os nomes são independentes por prédio.</small><button type="submit">Gravar andares deste prédio</button></div></form></details>{{end}}
{{else}}<div class="vazio"><strong>Nenhum prédio com mais de uma faixa identificada</strong>Confira o <a href="/painel/configuracao?aba=patrimonio">patrimônio</a> e as regras de IP dos grupos de implantação.</div>{{end}}</div>`))
