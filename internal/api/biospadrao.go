package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/biosstd"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// O padrão da BIOS (docs/bios-config.md, seção 9): três objetivos para a frota inteira, de
// qualquer marca e modelo. A senha de administrador pela guarda é a senha em lote
// (bioslote.go); o Wake on LAN ligado e o boot por USB desligado saem daqui, como jobs
// bios_settings com as mudanças que internal/biosstd resolve para cada máquina.

const padraoBase = "/painel/bios/padrao"

// Onde uma máquina está em cada objetivo.
const (
	stdOK      = "conforme"
	stdFix     = "corrigir" // há o que mudar, e a máquina pode receber o job agora
	stdReboot  = "reinicio" // a alteração já foi gravada; o inventário mostra o valor novo depois do reinício
	stdMissing = "sem"      // o modelo não informa a configuração
	stdOut     = "fora"     // não dá agora (o motivo vai junto)
)

var stdBucketLabel = map[string]string{
	stdOK: "Conforme", stdFix: "A corrigir", stdReboot: "Aguardando reinício", stdMissing: "O modelo não tem a configuração", stdOut: "Não dá agora",
}

const (
	// stdRebootWindow: por quanto tempo uma alteração concluída conta como "aguardando
	// reinício" enquanto o inventário mostra o valor antigo. Depois disso, volta a "a corrigir".
	stdRebootWindow = 7 * 24 * time.Hour
	// stdFailureWindow: uma alteração que falhou tira a máquina dos lotes por este tempo.
	stdFailureWindow = 24 * time.Hour
)

// stdPlan: uma máquina diante do padrão.
type stdPlan struct {
	lote     lotePlan
	settings store.BIOSFleetSettings
	// Gate: por que a máquina não pode receber um job de configuração agora ("" = pode).
	Gate     string
	findings map[string]biosstd.Finding
	rebooted map[string]bool // o objetivo já foi gravado e espera o reinício
}

func (p stdPlan) bucket(goal string) (string, string) {
	f := p.findings[goal]
	switch {
	case p.lote.Situation == loteBlocked:
		return stdOut, p.lote.Detail
	case !p.settings.Read:
		return stdOut, "o inventário não traz as configurações da BIOS desta máquina"
	}
	switch f.State {
	case biosstd.StateOK:
		return stdOK, ""
	case biosstd.StateMissing:
		return stdMissing, f.Note
	case biosstd.StateStuck:
		return stdOut, f.Note
	}
	switch {
	case p.rebooted[goal]:
		return stdReboot, fmt.Sprintf("job %d concluído", p.settings.LastJob)
	case p.Gate != "":
		return stdOut, p.Gate
	}
	return stdFix, ""
}

// newStdPlan avalia a máquina. changed: as alterações do último job concluído (nome → valor).
func newStdPlan(lp lotePlan, st store.BIOSFleetSettings, now time.Time) stdPlan {
	p := stdPlan{lote: lp, settings: st, findings: map[string]biosstd.Finding{}, rebooted: map[string]bool{}}
	for _, g := range biosstd.Goals {
		p.findings[g] = biosstd.Evaluate(g, st.Settings)
	}
	open := st.LastJob != 0 && !jobs.Final(st.LastState)
	switch lp.Situation {
	case loteUncertain:
		p.Gate = "há uma senha incerta na guarda: confira na BIOS qual vale antes"
	case loteRunning:
		p.Gate = "há uma gravação de senha a caminho (" + lp.Detail + ")"
	case loteMarker, loteDefault, lotePerson:
		p.Gate = "a BIOS tem senha e a guarda não tem a senha em uso: passe antes pela senha em lote"
	}
	if b := lp.facts.blocker(); p.Gate == "" && b != "" {
		p.Gate = b // uma máquina já na guarda que deixou de aceitar gravação (Sure Admin ligado, por exemplo)
	}
	switch {
	case p.Gate != "":
	case open:
		p.Gate = fmt.Sprintf("há uma alteração da BIOS a caminho (job %d)", st.LastJob)
	case st.LastState == jobs.StateFailed && st.LastFinished != nil && now.Sub(*st.LastFinished) < stdFailureWindow:
		p.Gate = fmt.Sprintf("a última alteração falhou (job %d): veja o motivo na máquina antes de repetir", st.LastJob)
	}
	// O que o último job concluído já gravou e o inventário ainda não mostra.
	if st.LastState == jobs.StateDone && st.LastFinished != nil && now.Sub(*st.LastFinished) < stdRebootWindow {
		if last, err := jobPayloadBIOSSettings(st.LastPayload); err == nil {
			done := map[string]string{}
			for _, c := range last.Changes {
				done[c.Name] = c.Value
			}
			for g, f := range p.findings {
				all := f.State == biosstd.StateFix
				for _, c := range f.Changes {
					all = all && done[c.Name] == c.Value
				}
				p.rebooted[g] = all
			}
		}
	}
	return p
}

// changes: as alterações dos objetivos escolhidos que a máquina pode receber agora.
func (p stdPlan) changes(goals []string) []biosconfig.Change {
	var out []biosconfig.Change
	seen := map[string]bool{}
	for _, g := range goals {
		if b, _ := p.bucket(g); b != stdFix {
			continue
		}
		for _, c := range p.findings[g].Changes {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c)
			}
		}
	}
	return out
}

type stdFilter struct {
	Goals []string
	Ring  int
	Limit int
}

func parseStdFilter(q url.Values) stdFilter {
	f := stdFilter{Limit: loteDefaultSize}
	for _, g := range biosstd.Goals {
		if slices.Contains(q["objetivo"], g) {
			f.Goals = append(f.Goals, g)
		}
	}
	if n, err := strconv.Atoi(q.Get("grupo")); err == nil && n > 0 && n < 1000 {
		f.Ring = n
	}
	if n, err := strconv.Atoi(q.Get("limite")); err == nil && slices.Contains(loteSizes, n) {
		f.Limit = n
	}
	return f
}

func (f stdFilter) values() url.Values {
	v := url.Values{"objetivo": f.Goals, "limite": {strconv.Itoa(f.Limit)}}
	if f.Ring != 0 {
		v.Set("grupo", strconv.Itoa(f.Ring))
	}
	return v
}

// stdTarget: uma máquina do lote, com o que vai ser gravado.
type stdTarget struct {
	plan    stdPlan
	changes []biosconfig.Change
}

func (f stdFilter) pick(plans []stdPlan) []stdTarget {
	var out []stdTarget
	for _, p := range plans {
		if f.Ring != 0 && p.lote.row.Ring != f.Ring {
			continue
		}
		ch := p.changes(f.Goals)
		if len(ch) == 0 || len(ch) > biosconfig.MaxChanges {
			continue
		}
		if len(out) == f.Limit {
			break
		}
		out = append(out, stdTarget{plan: p, changes: ch})
	}
	return out
}

func stdDigest(targets []stdTarget) string {
	h := sha256.New()
	for _, t := range targets {
		h.Write([]byte(t.plan.lote.row.MachineID))
		for _, c := range t.changes {
			h.Write([]byte("\x00" + c.Name + "\x00" + c.Value))
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// biosStdPlans lê a frota e avalia o padrão em cada máquina.
func (a *api) biosStdPlans(ctx context.Context) ([]stdPlan, error) {
	lote, _, err := a.biosFleetPlans(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := a.store.BIOSFleetSettings(ctx, biosstd.NamePattern, biosstd.NormStrip)
	if err != nil {
		return nil, err
	}
	now := a.now()
	out := make([]stdPlan, len(lote))
	for i, lp := range lote {
		out[i] = newStdPlan(lp, settings[lp.row.MachineID], now)
	}
	return out, nil
}

type stdCell struct {
	N    int
	Href string
}

type stdGoalRow struct {
	Key, Name string
	Checked   bool
	Cells     []stdCell // na ordem de stdBuckets
	Link      string    // a senha: o caminho é outro
}

type stdMapRow struct {
	Vendor, Name, From, To string
	N                      int
}

type stdMachineRow struct {
	ID, Name, Vendor, Ring string
	Changes                []string
	Detail                 string
}

type stdView struct {
	pageBase
	NoJobs    bool
	Total     int
	Buckets   []string
	Goals     []stdGoalRow
	Rings     []loteOption
	Sizes     []loteOption
	Chosen    bool
	Selected  int
	Digest    string
	Hidden    []loteHidden
	Mapping   []stdMapRow
	Preview   []stdMachineRow
	More      int
	MinReason int
	MaxReason int
	Listing   string
	ListRows  []stdMachineRow
	ListMore  int
}

var stdBuckets = []string{stdOK, stdFix, stdReboot, stdMissing, stdOut}

// biosStdPage: a conformidade da frota com o padrão e o lote de correção.
func (a *api) biosStdPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := stdView{pageBase: a.baseFor(r.Context(), s, "Padrão da BIOS", "configuracao"), NoJobs: a.jobKey == nil,
		MinReason: minReasonLength, MaxReason: maxReasonLength}
	q := r.URL.Query()
	stdFlash(q, &v)
	plans, err := a.biosStdPlans(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a frota para o padrão da BIOS", err)
		return
	}
	names, err := a.ringNames(r.Context())
	if err != nil {
		a.internalError(w, r, "ler os nomes dos grupos", err)
		return
	}
	f := parseStdFilter(q)
	v.Total = len(plans)
	for _, b := range stdBuckets {
		v.Buckets = append(v.Buckets, stdBucketLabel[b])
	}
	// A senha: a mesma contagem da senha em lote, nas colunas do padrão.
	pw := map[string]int{}
	rings := map[int]bool{}
	count := map[string]map[string]int{}
	for _, g := range biosstd.Goals {
		count[g] = map[string]int{}
	}
	for _, p := range plans {
		rings[p.lote.row.Ring] = true
		switch p.lote.Situation {
		case loteGuard:
			pw[stdOK]++
		case loteDefine, loteMarker, loteDefault:
			pw[stdFix]++
		case loteRunning:
			pw[stdReboot]++
		default:
			pw[stdOut]++
		}
		for _, g := range biosstd.Goals {
			b, _ := p.bucket(g)
			count[g][b]++
		}
	}
	senha := stdGoalRow{Key: "senha", Name: "Senha de administrador pela guarda", Link: loteBase}
	for _, b := range stdBuckets {
		senha.Cells = append(senha.Cells, stdCell{N: pw[b]})
	}
	v.Goals = append(v.Goals, senha)
	for _, g := range biosstd.Goals {
		row := stdGoalRow{Key: g, Name: biosstd.GoalLabel[g], Checked: slices.Contains(f.Goals, g)}
		for _, b := range stdBuckets {
			row.Cells = append(row.Cells, stdCell{N: count[g][b], Href: padraoBase + "?ver=" + g + "." + b})
		}
		v.Goals = append(v.Goals, row)
	}
	v.Rings = []loteOption{{Value: "0", Label: "todos os grupos", Selected: f.Ring == 0}}
	for _, n := range slices.Sorted(mapKeys(rings)) {
		v.Rings = append(v.Rings, loteOption{Value: strconv.Itoa(n), Label: "grupo " + loteRing(n, names), Selected: f.Ring == n})
	}
	for _, n := range loteSizes {
		label := strconv.Itoa(n) + " máquinas"
		if n == 1 {
			label = "1 máquina (o piloto)"
		}
		v.Sizes = append(v.Sizes, loteOption{Value: strconv.Itoa(n), Label: label, Selected: f.Limit == n})
	}
	machine := func(p stdPlan) stdMachineRow {
		return stdMachineRow{ID: p.lote.row.MachineID, Name: cmp.Or(p.lote.row.Hostname, p.lote.row.MachineID),
			Vendor: dash(vendorLabel(p.lote.row.Interface)), Ring: loteRing(p.lote.row.Ring, names)}
	}
	current := func(p stdPlan, name string) string {
		for _, st := range p.settings.Settings {
			if st.Name == name {
				return st.Value
			}
		}
		return ""
	}

	if goal, bucket, ok := strings.Cut(q.Get("ver"), "."); ok && slices.Contains(biosstd.Goals, goal) && stdBucketLabel[bucket] != "" {
		v.Listing = biosstd.GoalLabel[goal] + " › " + strings.ToLower(stdBucketLabel[bucket])
		for _, p := range plans {
			b, detail := p.bucket(goal)
			if b != bucket {
				continue
			}
			if len(v.ListRows) == loteListRows {
				v.ListMore++
				continue
			}
			row := machine(p)
			row.Detail = detail
			row.Changes = p.findings[goal].Evidence
			v.ListRows = append(v.ListRows, row)
		}
	}
	if len(f.Goals) > 0 {
		chosen := f.pick(plans)
		v.Chosen, v.Selected, v.Digest = true, len(chosen), stdDigest(chosen)
		for k, vals := range f.values() {
			for _, val := range vals {
				v.Hidden = append(v.Hidden, loteHidden{k, val})
			}
		}
		slices.SortFunc(v.Hidden, func(x, y loteHidden) int { return strings.Compare(x.Name+x.Value, y.Name+y.Value) })
		// O mapeamento: o que o padrão vira em cada fabricante e modelo, com quantas máquinas.
		byMap := map[stdMapRow]int{}
		for i, t := range chosen {
			row := machine(t.plan)
			for _, c := range t.changes {
				from := current(t.plan, c.Name)
				row.Changes = append(row.Changes, fmt.Sprintf("%s: %s → %s", c.Name, from, c.Value))
				byMap[stdMapRow{Vendor: row.Vendor, Name: c.Name, From: from, To: c.Value}]++
			}
			if i < lotePreviewRows {
				v.Preview = append(v.Preview, row)
			}
		}
		v.More = max(0, len(chosen)-lotePreviewRows)
		for k, n := range byMap {
			k.N = n
			v.Mapping = append(v.Mapping, k)
		}
		slices.SortFunc(v.Mapping, func(x, y stdMapRow) int {
			return cmp.Or(strings.Compare(x.Vendor, y.Vendor), strings.Compare(x.Name, y.Name), strings.Compare(x.From, y.From), strings.Compare(x.To, y.To))
		})
	}
	a.render(w, r, http.StatusOK, "bios-padrao", v)
}

// biosStdAction confere o lote e cria um job bios_settings por máquina. A trilha do lote é
// gravada antes: sem ela, nada sai.
func (a *api) biosStdAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	f := parseStdFilter(r.PostForm)
	back := func(kv ...string) {
		q := f.values()
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		http.Redirect(w, r, padraoBase+"?"+q.Encode(), http.StatusSeeOther)
	}
	switch {
	case a.jobKey == nil:
		back("erro", "sem-jobs")
		return
	case len(f.Goals) == 0:
		back("erro", "vazio")
		return
	}
	reason := strings.Join(strings.Fields(r.PostFormValue("motivo")), " ")
	if n := len([]rune(reason)); n < minReasonLength || n > maxReasonLength {
		back("erro", "motivo")
		return
	}
	ctx := r.Context()
	plans, err := a.biosStdPlans(ctx)
	if err != nil {
		a.internalError(w, r, "ler a frota para o padrão da BIOS", err)
		return
	}
	chosen := f.pick(plans)
	switch {
	case len(chosen) == 0:
		back("erro", "vazio")
		return
	case stdDigest(chosen) != r.PostFormValue("lista"):
		back("erro", "mudou")
		return
	}
	digest := stdDigest(chosen)
	var goalNames []string
	for _, g := range f.Goals {
		goalNames = append(goalNames, biosstd.GoalLabel[g])
	}
	group := "todos os grupos"
	if f.Ring != 0 {
		group = "grupo " + strconv.Itoa(f.Ring)
	}
	ev := store.AuditEvent{At: a.now(), Username: s.Username, Source: s.Source, Action: "bios.padrao.lote", Target: "parque",
		Detail: texto.Clip(fmt.Sprintf("%d máquina(s): %s · %s · conferência %s · motivo: %s", len(chosen), strings.Join(goalNames, ", "), group, digest, reason), 1000),
		Remote: remoteIP(r), RequestID: RequestID(ctx)}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	err = a.store.RecordAudit(actx, ev)
	cancel()
	if err != nil {
		a.logger.Error("padrão da BIOS não enviado: a auditoria não gravou", "error", err, "request_id", ev.RequestID)
		back("erro", "auditoria")
		return
	}
	a.logger.Warn("padrão da BIOS pedido em lote", "maquinas", len(chosen), "objetivos", strings.Join(f.Goals, ","), "conferencia", digest,
		"username", s.Username, "request_id", ev.RequestID)

	done, skipped := 0, 0
	for _, t := range chosen {
		id := t.plan.lote.row.MachineID
		risk := false
		var what []string
		for _, c := range t.changes {
			rule, _ := biosconfig.Lookup(c.Name)
			risk = risk || rule.Risk
			what = append(what, c.Name+" → "+c.Value)
		}
		var jobID int64
		err := errors.New("alteração de risco alto fica para a tela da máquina")
		if !risk {
			jobID, err = a.issueBIOSSettings(ctx, id, t.plan.lote.facts, t.plan.lote.active, t.changes, s.Username)
		}
		if err != nil {
			skipped++
			a.logger.Warn("padrão da BIOS: máquina pulada", "machine_id", id, "error", err, "request_id", ev.RequestID)
			continue
		}
		done++
		source := "BIOS sem senha"
		if t.plan.lote.active != nil {
			source = fmt.Sprintf("senha da guarda #%d", t.plan.lote.active.ID)
		}
		a.audit(r, s, "bios.config.job", id, texto.Clip(fmt.Sprintf("job %d, %s; padrão da BIOS, lote %s; motivo: %s; %s", jobID, source, digest, reason,
			strings.Join(what, "; ")), 1000))
	}
	http.Redirect(w, r, padraoBase+"?feitas="+strconv.Itoa(done)+"&puladas="+strconv.Itoa(skipped), http.StatusSeeOther)
}

func stdFlash(q url.Values, v *stdView) {
	if n, err := strconv.Atoi(q.Get("feitas")); err == nil && q.Has("feitas") {
		sk, _ := strconv.Atoi(q.Get("puladas"))
		v.Flash = fmt.Sprintf("Alteração pedida para %d máquina(s). Cada uma grava no próximo check-in; em vários modelos o valor novo só vale (e só aparece no inventário) depois do reinício.", n)
		if sk > 0 {
			v.Flash += fmt.Sprintf(" %d ficaram de fora (o motivo está no log do servidor).", sk)
		}
	}
	v.Error = map[string]string{
		"sem-jobs":  "O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): a alteração vai como job assinado.",
		"vazio":     "Nenhuma máquina neste lote: marque ao menos um objetivo com máquinas a corrigir.",
		"motivo":    fmt.Sprintf("Informe o motivo (de %d a %d caracteres): o chamado ou a ordem de serviço.", minReasonLength, maxReasonLength),
		"mudou":     "A lista mudou desde a conferência (alguma máquina mudou de situação). Confira de novo e repita o pedido.",
		"auditoria": "A trilha de auditoria não gravou: nada foi enviado.",
	}[q.Get("erro")]
}
