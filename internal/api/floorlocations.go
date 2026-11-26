package api

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
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
	Regex              bool
	Spec, Name, Legacy string
	Machines           int
}

type floorBuildingView struct {
	Building             string
	Rows                 []floorLocationRow
	Machines, Named      int
	Unmatched, Conflicts int
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
	assets, err := a.assetMap(ctx)
	if err != nil {
		return nil, err
	}
	for _, asset := range assets {
		if asset.Building != "" {
			counts[asset.Building] = map[string]int{}
		}
	}
	compiled := compileFloorRegex(names)
	totals, conflicts, unmatched := map[string]int{}, map[string]int{}, map[string]int{}
	for _, e := range fleet {
		b := e.Asset.Building
		if b == "" {
			continue
		}
		if counts[b] == nil {
			counts[b] = map[string]int{}
		}
		totals[b]++
		matches := matchingFloorRegex(e, compiled[b])
		if len(matches) > 0 {
			for _, match := range matches {
				counts[b][match.Spec]++
			}
			if _, conflict := regexFloorName(matches); conflict {
				conflicts[b]++
			}
			continue
		}
		if r := floorIP(e, rules.ips); r != nil {
			counts[b][r.Spec]++
		} else {
			unmatched[b]++
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
		row := floorBuildingView{Building: b, Machines: totals[b], Conflicts: conflicts[b], Unmatched: unmatched[b]}
		for spec, n := range specs {
			name := names[store.FloorKey{Building: b, Spec: spec}]
			row.Rows = append(row.Rows, floorLocationRow{Spec: spec, Name: name, Legacy: legacy[spec], Machines: n, Regex: strings.HasPrefix(spec, floorRegexPrefix)})
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
	if b == "" || len(specs) != len(names) || len(specs) > 500 {
		fail("formulário inválido")
		return
	}
	view, err := a.floorLocationsView(r.Context())
	if err != nil {
		a.internalError(w, r, "ler localizações", err)
		return
	}
	allowed := map[string]bool{}
	buildingFound := false
	for _, building := range view {
		if building.Building == b {
			buildingFound = true
			for _, row := range building.Rows {
				allowed[row.Spec] = true
			}
		}
	}
	if !buildingFound {
		fail("prédio não identificado")
		return
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
	pattern, newName := strings.TrimSpace(r.PostFormValue("regex")), strings.TrimSpace(r.PostFormValue("novo_nome"))
	if pattern != "" || newName != "" {
		spec := floorRegexPrefix + pattern
		if pattern == "" || newName == "" || len(spec) > 256 || !utf8.ValidString(newName) || utf8.RuneCountInString(newName) > 40 || strings.IndexFunc(newName, unicode.IsControl) >= 0 {
			fail("informe uma expressão regular e um nome de andar válidos")
			return
		}
		re, err := regexp.Compile(pattern)
		if err != nil || re.MatchString("") {
			fail("expressão regular inválida ou aceita texto vazio")
			return
		}
		if slices.ContainsFunc(list, func(l store.FloorLocation) bool { return l.Spec == spec }) {
			fail("expressão já cadastrada; edite o nome na lista")
			return
		}
		list = append(list, store.FloorLocation{FloorKey: store.FloorKey{Building: b, Spec: spec}, Name: newName})
		detail = append(detail, fmt.Sprintf("%s=%q", spec, newName))
	}
	if len(list) == 0 {
		fail("nenhuma regra informada")
		return
	}
	if err := db.SaveFloorLocations(r.Context(), list, s.Username); err != nil {
		a.internalError(w, r, "gravar localizações", err)
		return
	}
	a.audit(r, s, "localizacoes.andares", b, strings.Join(detail, "; "))
	back(url.Values{"gravado": {"1"}})
}

var _ = template.Must(pages.New("localizacoes").Parse(`<div class="ux-locations"><header class="ux-hero"><div><span class="ux-eyebrow">Organização do parque</span><h2>Prédios e andares</h2><p>Associe as redes aos pavimentos físicos de cada prédio.</p></div><div class="ux-hero-side"><span class="ux-status">{{len .Locations}} prédio(s) identificado(s)</span></div></header>
<details class="locations-help"><summary>Como organizar os andares</summary><p>Abra um prédio e cadastre uma expressão regular para cada andar. Use o mesmo nome para reunir várias redes no mesmo andar. Regras que apontam para andares distintos geram conflito e não atribuem andar. As faixas antigas continuam como alternativa quando nenhuma expressão corresponder.</p></details>
{{if .Locations}}<p class="locations-list-hint">Selecione um prédio para consultar ou editar suas regras.</p><div class="locations-list">{{range .Locations}}<details class="lugar location-building"><summary>
<span class="location-building-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 21V5l8-3v19M12 8h8v13M2 21h20M8 7v1M8 11v1M8 15v1M16 12v1M16 16v1"/></svg></span>
<span class="location-building-title"><b>{{localNome .Building}}</b><small>{{.Machines}} computador(es) · {{len .Rows}} regra(s)</small></span>
<span class="location-building-stats">{{if .Conflicts}}<span class="location-stat location-stat-danger">{{.Conflicts}} em conflito</span>{{end}}{{if .Unmatched}}<span class="location-stat location-stat-warn">{{.Unmatched}} sem regra</span>{{else}}<span class="location-stat">{{.Named}} nomeada(s)</span>{{end}}</span>
<svg class="location-chevron" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg></summary>
<form method="post" action="/painel/configuracao/localizacoes"><input type="hidden" name="predio" value="{{.Building}}">
<div class="location-content">{{if .Rows}}<div class="location-section-heading"><h3>Regras cadastradas</h3><small>Altere os nomes ou deixe vazio para remover o vínculo.</small></div>
<div class="location-table"><table><thead><tr><th scope="col">Regra de IP</th><th scope="col">Computadores</th><th scope="col">Nome do andar</th></tr></thead><tbody>
{{range .Rows}}<tr><td><small>{{if .Regex}}Expressão regular{{else}}Faixa de IP{{end}}</small><code>{{.Spec}}</code><input type="hidden" name="faixa" value="{{.Spec}}"></td><td class="num">{{.Machines}}</td><td><input aria-label="Nome do andar da faixa {{.Spec}}" name="nome" value="{{.Name}}" maxlength="40" placeholder="{{if .Legacy}}{{.Legacy}}{{else}}Ex.: Térreo, 1º andar{{end}}">{{if .Legacy}}<small class="muted">Nome anterior: {{.Legacy}}</small>{{end}}</td></tr>{{end}}
</tbody></table></div>{{else}}<div class="location-empty"><b>Nenhuma regra cadastrada</b><span>Adicione a primeira regra para identificar os andares deste prédio.</span></div>{{end}}
<div class="location-editor"><div class="location-section-heading"><span class="location-add-icon" aria-hidden="true">+</span><div><h3>Adicionar regra de andar</h3><small>Preencha o nome e a expressão que identifica o IPv4.</small></div></div>
<div class="location-fields"><label><span>Nome do andar</span><input name="novo_nome" maxlength="40" placeholder="Ex.: 1º andar"></label><label><span>Expressão regular do IPv4</span><input class="location-regex" name="regex" maxlength="250" spellcheck="false" autocomplete="off" placeholder="^10\.12\.1\.[0-9]+$"></label></div>
<div class="location-example"><span>Exemplo Go/RE2</span><code>^10\.12\.1\.[0-9]+$</code><span>Corresponde a 10.12.1.x</span></div></div></div>
<div class="location-footer"><small>As alterações são aplicadas somente a este prédio.</small><button type="submit">Gravar andares deste prédio</button></div></form></details>{{end}}</div>
{{else}}<div class="vazio"><strong>Nenhum prédio identificado no patrimônio</strong>Confira o <a href="/painel/configuracao?aba=patrimonio">patrimônio</a>.</div>{{end}}</div>`))
