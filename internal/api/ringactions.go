package api

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/patrimonio"
	"github.com/tasantiago/patchd/internal/store"
)

// Ações da página do grupo de implantação. Mover passa sempre por uma conferência que diz de
// que grupos as máquinas saem; o que se grava é a lista fechada que se viu. Regra nova (prédio
// ou faixa) mostra antes quantas máquinas ela move.

const (
	maxMoveBody   = 512 << 10 // 2000 IDs na conferência, com folga
	maxPasteLines = 5000
	maxPasteBytes = 256 << 10
	maxSimilar    = 3  // sugestões por linha não achada
	maxSuggested  = 50 // linhas não achadas que recebem sugestão (a distância custa)
	moveShown     = 100
)

// backToGroup: a página do grupo, na aba, com o resultado.
func backToGroup(w http.ResponseWriter, r *http.Request, ring int, tab string, q url.Values) {
	http.Redirect(w, r, groupPath(ring, tab, q), http.StatusSeeOther) // #nosec G710 -- caminho local montado aqui
}

type moveFrom struct {
	Label string
	N     int
}

type moveView struct {
	pageBase
	Dest             int
	DestLabel        string
	Back             string
	BackRing         int
	BackTab          string
	IDs              []string
	From             []moveFrom
	Already, Unknown int
	Names            []string
	More             int
	FilterTotal, Max int
	PinnedNote       bool
}

// moveIDs: as máquinas do pedido de mover. Marcadas (sel) ou, com filtro=1, as da lista da
// aba de onde veio, pelo mesmo filtro; confirmadas (id) depois da conferência.
func (a *api) moveIDs(r *http.Request, g groupFleet, back int) (ids []string, filterTotal int) {
	field := "sel"
	if r.PostFormValue("confirmar") == "1" {
		field = "id"
	}
	if field == "sel" && r.PostFormValue("filtro") == "1" {
		q := parseGroupQuery(r.PostForm)
		for _, e := range g.groupList(back, q) {
			ids = append(ids, e.ID)
		}
		filterTotal = len(ids)
		if len(ids) > maxBatch {
			ids = ids[:maxBatch]
		}
		return ids, filterTotal
	}
	seen := map[string]bool{}
	for _, id := range r.PostForm[field] {
		if machineIDPattern.MatchString(id) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, 0
}

// moveAction: POST /painel/grupos-implantacao/mover. Sem confirmar=1, a conferência.
func (a *api) moveAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMoveBody)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=lote-grande", http.StatusSeeOther)
		return
	}
	back, ok := formRing(r, "volta")
	if !ok {
		back = store.DefaultRing
	}
	tab := r.PostFormValue("aba")
	if !slices.Contains(groupTabs, tab) {
		tab = "maquinas"
	}
	dest, ok := formRing(r, "destino")
	if !ok {
		backToGroup(w, r, back, tab, url.Values{"erro": {"anel"}})
		return
	}
	if r.PostFormValue("confirmar") != "1" && r.PostFormValue("filtro") != "1" && len(r.PostForm["sel"]) > maxBatch {
		backToGroup(w, r, back, tab, url.Values{"erro": {"lote-grande"}})
		return
	}
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas", err)
		return
	}
	ids, filterTotal := a.moveIDs(r, g, back)
	if len(ids) > maxBatch {
		backToGroup(w, r, back, tab, url.Values{"erro": {"lote-grande"}})
		return
	}
	byID := map[string]equip{}
	for _, e := range g.all {
		byID[e.ID] = e
	}
	var chosen []equip
	already, unknown := 0, 0
	for _, id := range ids {
		e, ok := byID[id]
		switch {
		case !ok:
			unknown++
		case e.Ring == dest:
			already++
		default:
			chosen = append(chosen, e)
		}
	}
	if len(chosen) == 0 {
		backToGroup(w, r, back, tab, url.Values{"erro": {"lote-vazio"}})
		return
	}
	if r.PostFormValue("confirmar") != "1" {
		v := moveView{pageBase: a.baseFor(r.Context(), s, "Mover para o grupo de implantação", "aneis"), Dest: dest,
			DestLabel: groupLabel(dest, g.names), Back: groupPath(back, tab, nil), BackRing: back, BackTab: tab,
			Already: already, Unknown: unknown, FilterTotal: filterTotal, Max: maxBatch}
		count := map[int]int{}
		for _, e := range chosen {
			v.IDs = append(v.IDs, e.ID)
			count[e.Ring]++
			if len(v.Names) < moveShown {
				v.Names = append(v.Names, dash(e.Name))
			}
			if d := g.rules.decide(e); d.OK && d.Ring != dest {
				v.PinnedNote = true
			}
		}
		v.More = len(chosen) - len(v.Names)
		for _, ring := range slices.Sorted(maps.Keys(count)) {
			v.From = append(v.From, moveFrom{Label: groupLabel(ring, g.names), N: count[ring]})
		}
		if filterTotal <= maxBatch {
			v.FilterTotal = 0
		}
		a.render(w, r, http.StatusOK, "grupo-mover", v)
		return
	}
	moved := 0
	for _, e := range chosen {
		changed, err := a.store.SetMachineRing(r.Context(), e.ID, dest)
		if err != nil {
			a.internalError(w, r, "mudar grupo de implantação", err)
			return
		}
		if changed {
			moved++
		}
	}
	a.logger.Info("grupo de implantação alterado pela página do grupo", "ring", dest, "movidas", moved, "ja_estavam", already,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "maquina.anel_lote", fmt.Sprintf("grupo de implantação %d", dest),
		fmt.Sprintf("pela página do grupo: %d movida(s), %d já estavam", moved, already))
	backToGroup(w, r, back, tab, url.Values{"lote": {"1"}, "movidas": {strconv.Itoa(moved)}, "anel": {strconv.Itoa(dest)}})
}

// returnAction: POST /painel/grupos-implantacao/devolver. As marcadas deixam de ser "à mão" e
// as regras valem de novo para elas.
func (a *api) returnAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMoveBody)
	_ = r.ParseForm()
	back, ok := formRing(r, "volta")
	if !ok {
		back = store.DefaultRing
	}
	var ids []string
	for _, id := range r.PostForm["sel"] {
		if machineIDPattern.MatchString(id) && len(ids) < maxBatch {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		backToGroup(w, r, back, "maquinas", url.Values{"erro": {"lote-vazio"}})
		return
	}
	unpinned, err := a.store.UnpinRings(r.Context(), ids)
	if err != nil {
		a.internalError(w, r, "devolver à regra", err)
		return
	}
	moved, err := a.applyRingRules(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar as regras dos grupos", err)
		return
	}
	a.logger.Info("máquinas devolvidas às regras", "devolvidas", unpinned, "movidas", moved, "username", s.Username,
		"request_id", RequestID(r.Context()))
	a.audit(r, s, "maquina.anel_devolver", fmt.Sprintf("grupo de implantação %d", back),
		fmt.Sprintf("%d devolvida(s) às regras · %d mudaram de grupo", unpinned, moved))
	backToGroup(w, r, back, "maquinas", url.Values{"devolvidas": {strconv.Itoa(unpinned)}, "movidas": {strconv.Itoa(moved)}})
}

// --- Colar lista --------------------------------------------------------------------

type pasteRow struct {
	Line             int
	Token, Name, How string
	Group            string
}

type pasteMiss struct {
	Line    int
	Token   string
	Similar []string
}

type pasteView struct {
	pageBase
	Dest      int
	DestLabel string
	Back      string
	Found     []pasteRow
	Already   []pasteRow
	Missing   []pasteMiss
	Ambiguous []pasteMiss // Similar: as máquinas que o tombo acha
	Repeated  int
	IDs       []string
	Lines     int
}

// pasteToken: a primeira célula da linha (vale colar linhas inteiras da planilha), sem o
// domínio do nome.
func pasteToken(line string) string {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if i := strings.IndexAny(line, "\t;,"); i >= 0 {
		line = line[:i]
	}
	line = strings.Trim(strings.TrimSpace(line), `"'`)
	if f := strings.Fields(line); len(f) > 0 {
		line = f[0]
	}
	return line
}

// shortName: o nome sem o domínio, em minúsculas (PC01.tj.local → pc01).
func shortName(s string) string {
	s = strings.ToLower(s)
	if _, err := netip.ParseAddr(s); err == nil {
		return s
	}
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	return s
}

// distance: a distância de edição, parando em limit+1 (só interessa se é pequena).
func distance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// resolvePaste acha cada linha: pelo nome (sem domínio, sem diferença de maiúsculas), pelo IP,
// ou pelo tombo (os dígitos, como no patrimônio). O que não acha ganha sugestões de nomes
// parecidos.
func resolvePaste(all []equip, text string, dest int, names map[int]string) (v pasteView) {
	byName := map[string][]equip{}
	byTombo := map[string][]equip{}
	byIP := map[netip.Addr][]equip{}
	for _, e := range all {
		byName[shortName(e.Name)] = append(byName[shortName(e.Name)], e)
		for _, t := range patrimonio.Tombos(e.Name) {
			byTombo[t] = append(byTombo[t], e)
		}
		for _, ip := range e.ips {
			byIP[ip] = append(byIP[ip], e)
		}
	}
	seen := map[string]bool{}
	add := func(line int, token string, e equip, how string) {
		if seen[e.ID] {
			v.Repeated++
			return
		}
		seen[e.ID] = true
		row := pasteRow{Line: line, Token: token, Name: dash(e.Name), How: how, Group: groupLabel(e.Ring, names)}
		if e.Ring == dest {
			v.Already = append(v.Already, row)
			return
		}
		v.Found = append(v.Found, row)
		v.IDs = append(v.IDs, e.ID)
	}
	suggested := 0
	for i, raw := range strings.Split(text, "\n") {
		if i >= maxPasteLines {
			break
		}
		token := pasteToken(raw)
		if token == "" {
			continue
		}
		v.Lines++
		line := i + 1
		key := shortName(token)
		if m := byName[key]; len(m) == 1 {
			add(line, token, m[0], "nome")
			continue
		} else if len(m) > 1 {
			v.Ambiguous = append(v.Ambiguous, pasteMiss{Line: line, Token: token, Similar: equipNames(m)})
			continue
		}
		if ip, err := netip.ParseAddr(token); err == nil {
			if m := byIP[ip.Unmap()]; len(m) == 1 {
				add(line, token, m[0], "IP")
				continue
			} else if len(m) > 1 {
				v.Ambiguous = append(v.Ambiguous, pasteMiss{Line: line, Token: token, Similar: equipNames(m)})
				continue
			}
		}
		matched := false
		for _, t := range patrimonio.Tombos(token) {
			if m := byTombo[t]; len(m) == 1 {
				add(line, token, m[0], "tombo "+t)
				matched = true
				break
			} else if len(m) > 1 {
				v.Ambiguous = append(v.Ambiguous, pasteMiss{Line: line, Token: token, Similar: equipNames(m)})
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		miss := pasteMiss{Line: line, Token: token}
		if suggested < maxSuggested {
			suggested++
			type cand struct {
				name string
				d    int
			}
			var cs []cand
			for n, m := range byName {
				if d := distance(key, n, 2); d <= 2 {
					cs = append(cs, cand{dash(m[0].Name), d})
				}
			}
			slices.SortFunc(cs, func(x, y cand) int { return cmp.Or(cmp.Compare(x.d, y.d), cmp.Compare(x.name, y.name)) })
			for _, c := range cs[:min(len(cs), maxSimilar)] {
				miss.Similar = append(miss.Similar, c.name)
			}
		}
		v.Missing = append(v.Missing, miss)
	}
	return v
}

func equipNames(list []equip) []string {
	var out []string
	for _, e := range list {
		out = append(out, dash(e.Name))
	}
	slices.Sort(out)
	return out
}

// pasteAction: POST /painel/grupos-implantacao/colar. Mostra o que achou; o botão leva as
// achadas para a conferência do mover, já como lista fechada.
func (a *api) pasteAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPasteBytes+4<<10)
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=colar-grande", http.StatusSeeOther)
		return
	}
	dest, ok := formRing(r, "destino")
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=anel", http.StatusSeeOther)
		return
	}
	text := r.PostFormValue("lista")
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		backToGroup(w, r, dest, "colar", url.Values{"erro": {"colar-vazia"}})
		return
	}
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas", err)
		return
	}
	v := resolvePaste(g.all, text, dest, g.names)
	v.pageBase = a.baseFor(r.Context(), s, "Colar lista", "aneis")
	v.Dest, v.DestLabel, v.Back = dest, groupLabel(dest, g.names), groupPath(dest, "colar", nil)
	if len(v.IDs) > maxBatch {
		v.IDs = v.IDs[:maxBatch]
		v.Error = fmt.Sprintf("A lista achou mais de %d máquinas; este lote leva as %d primeiras.", maxBatch, maxBatch)
	}
	a.render(w, r, http.StatusOK, "grupo-colar", v)
}

// --- Regras -------------------------------------------------------------------------

type rulePreview struct {
	pageBase
	Kind, Rule, RangeText              string
	Ring                               int
	Label                              string
	Matched, Move, Already, PinnedStay int
	OtherRule, Conflicts               int
	From                               []moveFrom
	Excecoes                           bool
	Action, Back                       string
	Hidden                             []optView
}

// previewRule conta o que a regra nova faz: com ela somada às de hoje, para onde vai cada
// máquina que ela pega.
func previewRule(g groupFleet, after ringRules, ring int, matches func(equip) bool, excecoes bool) rulePreview {
	p := rulePreview{Ring: ring, Excecoes: excecoes}
	count := map[int]int{}
	for _, e := range g.all {
		if !matches(e) {
			continue
		}
		p.Matched++
		d := after.decide(e)
		if d.Conflict != "" {
			p.Conflicts++
		}
		switch {
		case !d.OK:
		case d.Ring != ring:
			p.OtherRule++ // o prédio vence a faixa, ou uma faixa menor
		case e.Ring == ring:
			p.Already++
		case g.pins[e.ID] && !excecoes:
			p.PinnedStay++
		default:
			p.Move++
			count[e.Ring]++
		}
	}
	for _, r := range slices.Sorted(maps.Keys(count)) {
		p.From = append(p.From, moveFrom{Label: groupLabel(r, g.names), N: count[r]})
	}
	return p
}

func (a *api) renderPreview(w http.ResponseWriter, r *http.Request, s panel.Session, p rulePreview, g groupFleet) {
	p.pageBase = a.baseFor(r.Context(), s, "Conferir a regra", "aneis")
	p.Label = groupLabel(p.Ring, g.names)
	p.Back = groupPath(p.Ring, "regras", nil)
	a.render(w, r, http.StatusOK, "grupo-regra", p)
}

// unpinMatching: as máquinas que a regra pega deixam de ser "à mão" (a opção de incluir as
// movidas à mão).
func (a *api) unpinMatching(r *http.Request, g groupFleet, matches func(equip) bool) (int, error) {
	var ids []string
	for _, e := range g.all {
		if matches(e) && g.pins[e.ID] {
			ids = append(ids, e.ID)
		}
	}
	return a.store.UnpinRings(r.Context(), ids)
}

// setBuildingRingAction: POST /painel/grupos-implantacao/predio. Liga o prédio ao grupo, com a
// prévia antes; com excecoes=1, as movidas à mão do prédio voltam à regra.
func (a *api) setBuildingRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	ring, ok := formRing(r, "anel")
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=anel", http.StatusSeeOther)
		return
	}
	b, ok, err := a.formBuilding(r)
	if err != nil {
		a.internalError(w, r, "ler o patrimônio", err)
		return
	}
	if !ok {
		backToGroup(w, r, ring, "regras", url.Values{"erro": {"predio"}})
		return
	}
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas", err)
		return
	}
	excecoes := r.PostFormValue("excecoes") == "1"
	matches := func(e equip) bool { return e.Asset.Building == b }
	if r.PostFormValue("confirmar") != "1" {
		after := g.rules
		after.buildings = maps.Clone(g.rules.buildings)
		after.buildings[b] = ring
		p := previewRule(g, after, ring, matches, excecoes)
		p.Kind, p.Rule, p.Action = "predio", b, "/painel/grupos-implantacao/predio"
		p.Hidden = []optView{{Value: "anel", Label: strconv.Itoa(ring)}, {Value: "predio", Label: b}}
		if excecoes {
			p.Hidden = append(p.Hidden, optView{Value: "excecoes", Label: "1"})
		}
		a.renderPreview(w, r, s, p, g)
		return
	}
	if err := a.store.SetBuildingRing(r.Context(), store.BuildingRing{Building: b, Ring: ring, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "gravar o vínculo do prédio", err)
		return
	}
	unpinned := 0
	if excecoes {
		if unpinned, err = a.unpinMatching(r, g, matches); err != nil {
			a.internalError(w, r, "devolver as exceções à regra", err)
			return
		}
	}
	moved, err := a.applyRingRules(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar as regras dos grupos", err)
		return
	}
	a.logger.Info("prédio ligado a grupo de implantação", "predio", b, "ring", ring, "movidas", moved, "devolvidas", unpinned,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo", b, fmt.Sprintf("grupo de implantação %d · %d máquina(s) movida(s) · %d exceção(ões) devolvida(s)", ring, moved, unpinned))
	backToGroup(w, r, ring, "regras", url.Values{"vinculo": {"1"}, "anel": {strconv.Itoa(ring)}, "movidas": {strconv.Itoa(moved)}})
}

// formBuilding: o prédio do formulário, como está no patrimônio atual.
func (a *api) formBuilding(r *http.Request) (string, bool, error) {
	b := strings.TrimSpace(r.PostFormValue("predio"))
	if b == "" || len(b) > 4*store.MaxBuildingLen {
		return "", false, nil
	}
	assets, err := a.assetMap(r.Context())
	if err != nil {
		return "", false, err
	}
	for _, l := range assets {
		if l.Building == b {
			return b, true, nil
		}
	}
	return "", false, nil
}

// buildingLink: o vínculo do prédio do formulário.
func (a *api) buildingLink(r *http.Request) (store.BuildingRing, bool, error) {
	b := strings.TrimSpace(r.PostFormValue("predio"))
	links, err := a.store.BuildingRings(r.Context())
	if err != nil {
		return store.BuildingRing{}, false, err
	}
	i := slices.IndexFunc(links, func(l store.BuildingRing) bool { return l.Building == b })
	if b == "" || i < 0 {
		return store.BuildingRing{}, false, nil
	}
	return links[i], true, nil
}

// deleteBuildingRingAction remove o vínculo; ninguém muda de grupo.
func (a *api) deleteBuildingRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	l, ok, err := a.buildingLink(r)
	if err != nil {
		a.internalError(w, r, "listar os vínculos de prédio", err)
		return
	}
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=sem-vinculo", http.StatusSeeOther)
		return
	}
	if _, err := a.store.DeleteBuildingRing(r.Context(), l.Building); err != nil {
		a.internalError(w, r, "remover o vínculo do prédio", err)
		return
	}
	a.logger.Info("vínculo de prédio removido", "predio", l.Building, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo_remover", l.Building, "")
	backToGroup(w, r, l.Ring, "regras", url.Values{"vinculo-removido": {"1"}})
}

// unpinBuildingAction devolve à regra as máquinas do prédio movidas à mão.
func (a *api) unpinBuildingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	l, ok, err := a.buildingLink(r)
	if err != nil {
		a.internalError(w, r, "listar os vínculos de prédio", err)
		return
	}
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=sem-vinculo", http.StatusSeeOther)
		return
	}
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas", err)
		return
	}
	unpinned, err := a.unpinMatching(r, g, func(e equip) bool { return e.Asset.Building == l.Building })
	if err != nil {
		a.internalError(w, r, "devolver as exceções à regra", err)
		return
	}
	moved, err := a.applyRingRules(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar as regras dos grupos", err)
		return
	}
	a.logger.Info("exceções do prédio devolvidas ao vínculo", "predio", l.Building, "devolvidas", unpinned, "movidas", moved,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "predio.grupo_excecoes", l.Building, fmt.Sprintf("%d exceção(ões) devolvida(s) · %d máquina(s) movida(s)", unpinned, moved))
	backToGroup(w, r, l.Ring, "regras", url.Values{"devolvidas": {strconv.Itoa(unpinned)}, "movidas": {strconv.Itoa(moved)}})
}

// addIPRingAction: POST /painel/grupos-implantacao/faixa. A faixa nova, com a prévia antes.
func (a *api) addIPRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	ring, ok := formRing(r, "anel")
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=anel", http.StatusSeeOther)
		return
	}
	spec := strings.TrimSpace(r.PostFormValue("faixa"))
	rng, ok := parseIPRange(spec)
	if !ok || len(spec) > store.MaxIPSpecLen {
		backToGroup(w, r, ring, "regras", url.Values{"erro": {"faixa"}})
		return
	}
	g, err := a.loadGroupFleet(r.Context())
	if err != nil {
		a.internalError(w, r, "listar as máquinas", err)
		return
	}
	if slices.ContainsFunc(g.rules.ips, func(x ipRule) bool { return x.Spec == spec }) {
		backToGroup(w, r, ring, "regras", url.Values{"erro": {"faixa-repetida"}})
		return
	}
	excecoes := r.PostFormValue("excecoes") == "1"
	matches := func(e equip) bool { return e.inRange(rng) }
	if r.PostFormValue("confirmar") != "1" {
		after := g.rules
		after.ips = append(slices.Clone(g.rules.ips), ipRule{IPRing: store.IPRing{Spec: spec, Ring: ring}, rng: rng, size: addr4(rng.hi) - addr4(rng.lo)})
		p := previewRule(g, after, ring, matches, excecoes)
		p.Kind, p.Rule, p.Action = "faixa", spec, "/painel/grupos-implantacao/faixa"
		p.RangeText = rng.lo.String() + " – " + rng.hi.String()
		p.Hidden = []optView{{Value: "anel", Label: strconv.Itoa(ring)}, {Value: "faixa", Label: spec}}
		if excecoes {
			p.Hidden = append(p.Hidden, optView{Value: "excecoes", Label: "1"})
		}
		a.renderPreview(w, r, s, p, g)
		return
	}
	if _, err := a.store.AddIPRing(r.Context(), store.IPRing{Spec: spec, Ring: ring, UpdatedBy: s.Username}); err != nil {
		if errors.Is(err, store.ErrIPSpecTaken) {
			backToGroup(w, r, ring, "regras", url.Values{"erro": {"faixa-repetida"}})
			return
		}
		a.internalError(w, r, "gravar a faixa", err)
		return
	}
	unpinned := 0
	if excecoes {
		if unpinned, err = a.unpinMatching(r, g, matches); err != nil {
			a.internalError(w, r, "devolver as exceções à regra", err)
			return
		}
	}
	moved, err := a.applyRingRules(r.Context())
	if err != nil {
		a.internalError(w, r, "aplicar as regras dos grupos", err)
		return
	}
	a.logger.Info("faixa de IP ligada a grupo de implantação", "faixa", spec, "ring", ring, "movidas", moved, "devolvidas", unpinned,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "faixa.grupo", spec, fmt.Sprintf("grupo de implantação %d · %d máquina(s) movida(s) · %d exceção(ões) devolvida(s)", ring, moved, unpinned))
	backToGroup(w, r, ring, "regras", url.Values{"faixa-ok": {"1"}, "anel": {strconv.Itoa(ring)}, "movidas": {strconv.Itoa(moved)}})
}

// deleteIPRingAction: POST /painel/grupos-implantacao/faixa/remover. Ninguém muda de grupo.
func (a *api) deleteIPRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	id, err := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=sem-faixa", http.StatusSeeOther)
		return
	}
	old, ok, err := a.store.DeleteIPRing(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "remover a faixa", err)
		return
	}
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=sem-faixa", http.StatusSeeOther)
		return
	}
	a.logger.Info("faixa de IP de grupo removida", "faixa", old.Spec, "ring", old.Ring, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "faixa.grupo_remover", old.Spec, fmt.Sprintf("grupo de implantação %d", old.Ring))
	backToGroup(w, r, old.Ring, "regras", url.Values{"faixa-removida": {"1"}})
}

// ringNameAction: POST /painel/grupos-implantacao/nome. Vazio apaga o nome.
func (a *api) ringNameAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	ring, ok := formRing(r, "anel")
	if !ok {
		http.Redirect(w, r, "/painel/grupos-implantacao?erro=anel", http.StatusSeeOther)
		return
	}
	name := strings.Join(strings.Fields(r.PostFormValue("nome")), " ")
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > store.MaxRingNameLen || strings.ContainsFunc(name, func(c rune) bool { return c < ' ' }) {
		backToGroup(w, r, ring, "regras", url.Values{"erro": {"nome"}})
		return
	}
	if err := a.store.SetRingName(r.Context(), store.RingName{Ring: ring, Name: name, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "gravar o nome do grupo", err)
		return
	}
	a.audit(r, s, "grupo.nome", fmt.Sprintf("grupo de implantação %d", ring), name)
	backToGroup(w, r, ring, "regras", url.Values{"nome": {"1"}})
}
