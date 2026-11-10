package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// Senha da BIOS em lote (docs/bios-senha.md, seção 12). A tela da máquina prepara e grava uma
// senha por vez; aqui o admin escolhe as situações (sem senha, com a senha do .bat no
// Registro, com uma senha padrão), confere a lista e o servidor faz, máquina por máquina, o
// mesmo que a tela faz: sorteia a senha da guarda e cria o job bios_password. As regras da
// gravação não mudam: uma chamada ao fabricante por job, sem nova tentativa automática.

// As situações de uma máquina. As três primeiras entram num lote; as outras só aparecem na
// contagem.
const (
	loteDefine    = "definir"   // a BIOS está sem senha: a da guarda é definida
	loteMarker    = "marcador"  // com senha, e o Registro tem a que o .bat deixou (seção 11)
	loteDefault   = "padrao"    // com senha, sem marcador: a padrão do fabricante ou a digitada no lote
	loteGuard     = "guarda"    // a senha da guarda já vale
	loteRunning   = "andamento" // um job de gravação está a caminho
	loteUncertain = "incerta"   // há uma senha incerta: alguém confere na BIOS
	lotePerson    = "pessoa"    // precisa de uma decisão (a tela da máquina)
	loteBlocked   = "bloqueada" // o patchd não grava nesta máquina (ou ainda não sabe)
)

var loteSelectable = []string{loteDefine, loteMarker, loteDefault}

var loteLabel = map[string]struct{ name, does string }{
	loteDefine:    {"Sem senha na BIOS", "define a senha da guarda"},
	loteMarker:    {"Com a senha que o .bat deixou no Registro", "troca pela da guarda; o agente lê a atual na própria máquina e a apaga do Registro"},
	loteDefault:   {"Com senha, sem marcador", "troca pela da guarda, usando como atual a padrão do fabricante ou a digitada no lote"},
	loteGuard:     {"Já com a senha da guarda", "nada a fazer"},
	loteRunning:   {"Gravação a caminho", "o job espera o check-in da máquina (até 24 h)"},
	loteUncertain: {"Senha incerta", "alguém confere na BIOS qual vale, na tela da máquina"},
	lotePerson:    {"Precisa de uma pessoa", "a tela da máquina decide"},
	loteBlocked:   {"Fora do alcance", "o patchd não grava nesta máquina"},
}

// Limites do lote.
const (
	loteMax         = 500 // máquinas por lote: o pedido assina e cifra um job por máquina
	loteDefaultSize = 10
	lotePreviewRows = 50
	loteListRows    = 200
	// loteMaxFailures: com tantas falhas em loteFailureWindow, a máquina sai dos lotes. Senha
	// errada repetida trava a BIOS até reiniciar; a terceira tentativa é de uma pessoa.
	loteMaxFailures   = 2
	loteFailureWindow = 24 * time.Hour
)

var loteSizes = []int{1, 10, 50, 200, loteMax}

// lotePlan: uma máquina, classificada.
type lotePlan struct {
	row       store.BIOSFleetRow
	facts     biosFacts
	Situation string
	Detail    string              // o motivo, nas situações que não entram em lote
	pending   *store.BIOSPassword // a pendente sem job, que a gravação usa (nil: sorteia)
	Failed    bool                // a última tentativa falhou
	Failures  int                 // falhas na janela
	NeedTyped bool                // padrao: o fabricante não tem senha padrão cadastrada
}

// classifyBIOS decide a situação de uma máquina. fleetVendors: os fabricantes com senha padrão
// cadastrada e legível com a chave de guarda atual.
func classifyBIOS(row store.BIOSFleetRow, escrowID string, fleetVendors map[string]bool, now time.Time) lotePlan {
	p := lotePlan{row: row}
	if row.Interface != "" || row.AdminPassword != nil || row.SureAdmin != nil {
		p.facts.Firmware = &protocol.Firmware{Interface: row.Interface, AdminPassword: row.AdminPassword, SureAdmin: row.SureAdmin}
	}
	if pub, err := machinebox.ParsePublic(row.BoxKey); err == nil {
		p.facts.BoxKey, p.facts.BoxKeyID = pub, machinebox.KeyID(pub)
	}
	p.facts.CopyOK, p.facts.Legacy = row.CopyOK, row.Legacy

	var active, pending *store.BIOSPassword
	uncertain, lastSeen := false, false
	for i := range row.Passwords {
		b := &row.Passwords[i]
		switch b.State {
		case store.BIOSUncertain:
			uncertain = true
		case store.BIOSActive:
			active = b
		case store.BIOSPending:
			pending = b
		case store.BIOSFailed:
			if now.Sub(b.UpdatedAt) < loteFailureWindow {
				p.Failures++
			}
		}
		// A última tentativa: a linha mais nova que não é a pendente à espera.
		if !lastSeen && b.State != store.BIOSPending {
			lastSeen, p.Failed = true, b.State == store.BIOSFailed
		}
	}
	set := func(sit, detail string) lotePlan {
		p.Situation, p.Detail = sit, detail
		return p
	}
	switch {
	case uncertain:
		return set(loteUncertain, "há uma senha incerta: confira na BIOS qual vale")
	case pending != nil && pending.JobID != 0:
		return set(loteRunning, fmt.Sprintf("job %d", pending.JobID))
	case active != nil && active.EscrowKeyID == escrowID:
		return set(loteGuard, fmt.Sprintf("guarda #%d", active.ID))
	case active != nil:
		return set(lotePerson, "a senha ativa foi fechada com outra chave de guarda ("+active.EscrowKeyID+")")
	}
	if b := p.facts.blocker(); b != "" {
		return set(loteBlocked, b)
	}
	if pending != nil {
		if pending.EscrowKeyID != escrowID {
			return set(lotePerson, "a senha pendente foi fechada com outra chave de guarda ("+pending.EscrowKeyID+")")
		}
		p.pending = pending
	}
	switch {
	case !*p.facts.Firmware.AdminPassword:
		return set(loteDefine, "")
	case p.facts.legacyOK() && p.Failed:
		// A do marcador já foi recusada: tentar de novo é repetir a mesma senha errada.
		return set(lotePerson, "a BIOS recusou a senha do marcador do .bat: confira a senha em uso (o histórico do .bat pode ter a certa)")
	case p.facts.legacyOK():
		return set(loteMarker, "")
	}
	p.NeedTyped = !fleetVendors[row.Interface]
	return set(loteDefault, "")
}

// loteFilter: o que o admin escolheu.
type loteFilter struct {
	Sits   []string // as situações (loteSelectable)
	Failed bool     // incluir as que falharam na última tentativa
	Ring   int      // 0: todos os grupos
	Limit  int
}

func parseLoteFilter(q url.Values) loteFilter {
	f := loteFilter{Failed: q.Get("falhas") == "1", Limit: loteDefaultSize}
	for _, s := range q["sit"] {
		if slices.Contains(loteSelectable, s) && !slices.Contains(f.Sits, s) {
			f.Sits = append(f.Sits, s)
		}
	}
	slices.Sort(f.Sits)
	if n, err := strconv.Atoi(q.Get("grupo")); err == nil && n > 0 && n < 1000 {
		f.Ring = n
	}
	if n, err := strconv.Atoi(q.Get("limite")); err == nil && slices.Contains(loteSizes, n) {
		f.Limit = n
	}
	return f
}

func (f loteFilter) values() url.Values {
	v := url.Values{"sit": f.Sits, "limite": {strconv.Itoa(f.Limit)}}
	if f.Failed {
		v.Set("falhas", "1")
	}
	if f.Ring != 0 {
		v.Set("grupo", strconv.Itoa(f.Ring))
	}
	return v
}

// pick escolhe as máquinas do lote, na ordem do nome, até o limite. Uma máquina cuja última
// tentativa falhou só entra a pedido, abaixo do teto de falhas; as sem senha tentam de novo
// como estão, e as com senha exigem a senha digitada (outra padrão), conferida em runBIOSLote.
func (f loteFilter) pick(plans []lotePlan) []lotePlan {
	var out []lotePlan
	for _, p := range plans {
		switch {
		case !slices.Contains(f.Sits, p.Situation), f.Ring != 0 && p.row.Ring != f.Ring:
			continue
		case p.Failed && (!f.Failed || p.Failures >= loteMaxFailures):
			continue
		}
		if len(out) == f.Limit {
			break
		}
		out = append(out, p)
	}
	return out
}

// loteDigest: a impressão da lista conferida (as máquinas e a situação de cada uma).
func loteDigest(plans []lotePlan) string {
	h := sha256.New()
	for _, p := range plans {
		h.Write([]byte(p.row.MachineID + "\x00" + p.Situation + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// biosFleetPlans lê a frota e classifica cada máquina. Antes, fecha as gravações terminadas
// (um job expirado devolve a senha a pendente).
func (a *api) biosFleetPlans(ctx context.Context) ([]lotePlan, map[string]bool, error) {
	rows, err := a.store.BIOSFleet(ctx)
	if err != nil {
		return nil, nil, err
	}
	settled := false
	for _, r := range rows {
		for _, b := range r.Passwords {
			if b.State == store.BIOSPending && b.JobID != 0 {
				if err := a.settleBIOS(ctx, r.MachineID); err != nil {
					return nil, nil, err
				}
				settled = true
				break
			}
		}
	}
	if settled {
		if rows, err = a.store.BIOSFleet(ctx); err != nil {
			return nil, nil, err
		}
	}
	vendors := map[string]bool{}
	fleet, err := a.store.BIOSFleetPasswords(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range fleet {
		vendors[b.Vendor] = b.EscrowKeyID == a.escrow.id
	}
	plans := make([]lotePlan, len(rows))
	for i, r := range rows {
		plans[i] = classifyBIOS(r, a.escrow.id, vendors, a.now())
	}
	return plans, vendors, nil
}

type loteCount struct {
	Key, Name, Does string
	N               int
	Selectable      bool
	Checked         bool
	Href            string
}

type loteRow struct {
	ID, Name, Vendor, Ring string
	Situation, Detail      string
	Current                string // a senha atual que o lote usa
	Level                  string
}

type loteOption struct {
	Value, Label string
	Selected     bool
}

type loteView struct {
	pageBase
	On, NoJobs  bool
	KeyID       string
	Total       int
	Counts      []loteCount
	FailedCount int // máquinas cuja última tentativa falhou (em qualquer situação de lote)
	Failed      bool
	Rings       []loteOption
	Sizes       []loteOption
	// A prévia do lote escolhido.
	Chosen     bool
	Preview    []loteRow
	More       int
	Selected   int
	Digest     string
	Hidden     []loteHidden
	AskCurrent bool   // o lote tem máquinas com senha e sem marcador: o campo da senha digitada
	NeedTyped  int    // quantas só entram com a senha digitada
	Defaults   string // os fabricantes com senha padrão cadastrada
	MaxCurrent int
	MinReason  int
	MaxReason  int
	// A lista de uma situação (?ver=).
	Listing     string
	ListRows    []loteRow
	ListMore    int
	MaxFailures int
}

type loteHidden struct{ Name, Value string }

func (a *api) loteRow(p lotePlan, names map[int]string, typed bool) loteRow {
	r := loteRow{ID: p.row.MachineID, Name: cmp.Or(p.row.Hostname, p.row.MachineID), Vendor: dash(vendorLabel(p.row.Interface)),
		Ring: loteRing(p.row.Ring, names), Situation: loteLabel[p.Situation].name, Detail: p.Detail}
	switch p.Situation {
	case loteDefine:
		r.Current = "nenhuma (a BIOS está sem senha)"
	case loteMarker:
		r.Current = "a do Registro, lida pelo agente"
	case loteDefault:
		switch {
		case typed:
			r.Current = "a digitada neste lote"
		case p.NeedTyped || p.Failed:
			r.Current, r.Level = "só com a senha digitada abaixo", "atencao"
		default:
			r.Current = "a padrão da " + vendorLabel(p.row.Interface)
		}
	}
	if p.Failed {
		r.Detail = strings.TrimSpace(r.Detail + " a última tentativa falhou")
	}
	return r
}

func loteRing(ring int, names map[int]string) string {
	if n := names[ring]; n != "" {
		return fmt.Sprintf("%d · %s", ring, n)
	}
	return ringLabel(ring)
}

const loteBase = "/painel/bios/lote"

// biosLotePage mostra a contagem da frota, o formulário do lote e, com um lote escolhido, a
// prévia a conferir.
func (a *api) biosLotePage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := loteView{pageBase: a.baseFor(r.Context(), s, "Senha da BIOS em lote", "configuracao"), On: a.escrow != nil, NoJobs: a.jobKey == nil,
		MaxCurrent: maxCurrentPassword, MinReason: minRevealReason, MaxReason: maxRevealReason, MaxFailures: loteMaxFailures}
	q := r.URL.Query()
	loteFlash(q, &v)
	if !v.On {
		a.render(w, r, http.StatusOK, "bios-lote", v)
		return
	}
	v.KeyID = a.escrow.id
	plans, vendors, err := a.biosFleetPlans(r.Context())
	if err != nil {
		a.internalError(w, r, "ler a frota para a senha da BIOS em lote", err)
		return
	}
	names, err := a.ringNames(r.Context())
	if err != nil {
		a.internalError(w, r, "ler os nomes dos grupos", err)
		return
	}
	f := parseLoteFilter(q)
	v.Total, v.Failed = len(plans), f.Failed
	count, rings := map[string]int{}, map[int]bool{}
	for _, p := range plans {
		count[p.Situation]++
		rings[p.row.Ring] = true
		if p.Failed && slices.Contains(loteSelectable, p.Situation) {
			v.FailedCount++
		}
	}
	for _, k := range []string{loteDefine, loteMarker, loteDefault, loteRunning, loteGuard, loteUncertain, lotePerson, loteBlocked} {
		l := loteLabel[k]
		v.Counts = append(v.Counts, loteCount{Key: k, Name: l.name, Does: l.does, N: count[k], Selectable: slices.Contains(loteSelectable, k),
			Checked: slices.Contains(f.Sits, k), Href: loteBase + "?ver=" + k})
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
	var withDefault []string
	for _, vd := range jobs.BIOSVendors {
		if vendors[vd] {
			withDefault = append(withDefault, vendorLabel(vd))
		}
	}
	v.Defaults = strings.Join(withDefault, ", ")

	if ver := q.Get("ver"); loteLabel[ver].name != "" {
		v.Listing = loteLabel[ver].name
		for _, p := range plans {
			if p.Situation != ver {
				continue
			}
			if len(v.ListRows) == loteListRows {
				v.ListMore++
				continue
			}
			v.ListRows = append(v.ListRows, a.loteRow(p, names, false))
		}
	}
	if len(f.Sits) > 0 {
		chosen := f.pick(plans)
		v.Chosen, v.Selected, v.Digest = true, len(chosen), loteDigest(chosen)
		for k, vals := range f.values() {
			for _, val := range vals {
				v.Hidden = append(v.Hidden, loteHidden{k, val})
			}
		}
		slices.SortFunc(v.Hidden, func(x, y loteHidden) int { return strings.Compare(x.Name+x.Value, y.Name+y.Value) })
		for i, p := range chosen {
			if p.Situation == loteDefault {
				v.AskCurrent = true
				if p.NeedTyped || p.Failed {
					v.NeedTyped++
				}
			}
			if i < lotePreviewRows {
				v.Preview = append(v.Preview, a.loteRow(p, names, false))
			}
		}
		v.More = max(0, len(chosen)-lotePreviewRows)
	}
	a.render(w, r, http.StatusOK, "bios-lote", v)
}

func mapKeys(m map[int]bool) func(yield func(int) bool) {
	return func(yield func(int) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// biosLoteAction confere o lote e cria, para cada máquina, a senha da guarda e o job de
// gravação. A trilha do lote é gravada antes: sem ela, nada sai.
func (a *api) biosLoteAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	f := parseLoteFilter(r.PostForm)
	back := func(kv ...string) {
		q := f.values()
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		http.Redirect(w, r, loteBase+"?"+q.Encode(), http.StatusSeeOther)
	}
	switch {
	case a.escrow == nil:
		back("erro", "sem-chave")
		return
	case a.jobKey == nil:
		back("erro", "sem-jobs")
		return
	case len(f.Sits) == 0:
		back("erro", "vazio")
		return
	}
	reason := strings.Join(strings.Fields(r.PostFormValue("motivo")), " ")
	if n := len([]rune(reason)); n < minRevealReason || n > maxRevealReason {
		back("erro", "motivo")
		return
	}
	typed := r.PostFormValue("senha_atual")
	if len(typed) > maxCurrentPassword || strings.ContainsFunc(typed, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		back("erro", "senha")
		return
	}
	ctx := r.Context()
	plans, _, err := a.biosFleetPlans(ctx)
	if err != nil {
		a.internalError(w, r, "ler a frota para a senha da BIOS em lote", err)
		return
	}
	chosen := f.pick(plans)
	switch {
	case len(chosen) == 0:
		back("erro", "vazio")
		return
	case loteDigest(chosen) != r.PostFormValue("lista"):
		back("erro", "mudou") // a frota mudou entre a conferência e o pedido: conferir de novo
		return
	}
	// As senhas padrão cadastradas, abertas uma vez por fabricante.
	defaults := map[string]string{}
	defer func() { clear(defaults) }()
	per := map[string]int{}
	for _, p := range chosen {
		per[p.Situation]++
		if p.Situation != loteDefault {
			continue
		}
		vendor := p.row.Interface
		switch {
		case typed != "" && biospw.CheckFleet(vendor, typed) != nil:
			back("erro", "senha")
			return
		case typed != "":
			continue
		case p.NeedTyped || p.Failed:
			back("erro", "falta-senha")
			return
		}
		if _, ok := defaults[vendor]; !ok {
			pw, b, err := a.fleetPassword(ctx, vendor)
			if err != nil || b == nil {
				a.logger.Error("senha padrão da BIOS no lote", "fabricante", vendor, "error", err, "request_id", RequestID(ctx))
				back("erro", "envelope")
				return
			}
			defaults[vendor] = pw
		}
	}

	digest := loteDigest(chosen)
	var parts []string
	for _, k := range loteSelectable {
		if per[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", per[k], map[string]string{loteDefine: "sem senha", loteMarker: "com a senha do .bat no Registro",
				loteDefault: "com senha padrão"}[k]))
		}
	}
	origin := ""
	if per[loteDefault] > 0 {
		origin = "; senha atual das com padrão: a cadastrada do fabricante"
		if typed != "" {
			origin = "; senha atual das com padrão: digitada pelo admin"
		}
	}
	group := "todos os grupos"
	if f.Ring != 0 {
		group = "grupo " + strconv.Itoa(f.Ring)
	}
	ev := store.AuditEvent{At: a.now(), Username: s.Username, Source: s.Source, Action: "bios.senha.lote", Target: "parque",
		Detail: texto.Clip(fmt.Sprintf("%d máquina(s): %s%s · %s · conferência %s · motivo: %s", len(chosen), strings.Join(parts, ", "), origin, group,
			digest, reason), 1000),
		Remote: remoteIP(r), RequestID: RequestID(ctx)}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	err = a.store.RecordAudit(actx, ev)
	cancel()
	if err != nil {
		a.logger.Error("senha da BIOS em lote não enviada: a auditoria não gravou", "error", err, "request_id", ev.RequestID)
		back("erro", "auditoria")
		return
	}
	a.logger.Warn("senha da BIOS em lote pedida", "maquinas", len(chosen), "conferencia", digest, "username", s.Username, "request_id", ev.RequestID)

	done, skipped := 0, 0
	for _, p := range chosen {
		id := p.row.MachineID
		skip := func(why string, err error) {
			skipped++
			a.logger.Warn("senha da BIOS em lote: máquina pulada", "machine_id", id, "motivo", why, "error", err, "request_id", ev.RequestID)
		}
		target := p.pending
		if target == nil {
			b, created, err := a.prepareBIOS(ctx, id, s.Username)
			if err != nil {
				skip("a senha não pôde ser guardada", err)
				continue
			}
			if created {
				a.audit(r, s, "bios.senha.preparar", id, fmt.Sprintf("guarda #%d, chave %s · lote %s", b.ID, a.escrow.id, digest))
			}
			if b.JobID != 0 || b.EscrowKeyID != a.escrow.id {
				skip("outra gravação chegou antes", nil)
				continue
			}
			target = &b
		}
		var cur biosCurrent
		source := "definição (a BIOS estava sem senha)"
		switch p.Situation {
		case loteMarker:
			cur.from, source = jobs.BIOSCurrentMarker, "troca (senha atual: a que o .bat deixou no Registro da máquina, lida pelo agente)"
		case loteDefault:
			if typed != "" {
				cur.pw, source = []byte(typed), "troca (senha atual informada pelo admin no lote)"
			} else {
				cur.pw, source = []byte(defaults[p.row.Interface]), "troca (senha atual: a padrão da "+vendorLabel(p.row.Interface)+")"
			}
		}
		jobID, ok, err := a.issueBIOSWrite(ctx, id, *target, p.facts, cur, s.Username)
		if err != nil || !ok {
			skip("o job não foi criado", err)
			continue
		}
		done++
		a.audit(r, s, "bios.senha.gravar", id, fmt.Sprintf("guarda #%d, job %d, %s · lote %s", target.ID, jobID, source, digest))
	}
	http.Redirect(w, r, loteBase+"?feitas="+strconv.Itoa(done)+"&puladas="+strconv.Itoa(skipped), http.StatusSeeOther)
}

// loteFlash: os avisos da tela.
func loteFlash(q url.Values, v *loteView) {
	if n, err := strconv.Atoi(q.Get("feitas")); err == nil && q.Has("feitas") {
		sk, _ := strconv.Atoi(q.Get("puladas"))
		v.Flash = fmt.Sprintf("Gravação pedida para %d máquina(s). Cada uma grava no próximo check-in e responde no cartão Senha da BIOS; a contagem abaixo acompanha.", n)
		if sk > 0 {
			v.Flash += fmt.Sprintf(" %d ficaram de fora (mudaram de situação entre a conferência e o pedido; o motivo está no log do servidor).", sk)
		}
	}
	v.Error = map[string]string{
		"sem-chave":   "Guarda desligada: o servidor está sem a chave de guarda.",
		"sem-jobs":    "O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): a gravação vai como job assinado.",
		"vazio":       "Nenhuma máquina neste lote: marque ao menos uma situação com máquinas.",
		"motivo":      fmt.Sprintf("Informe o motivo (de %d a %d caracteres): o chamado ou a ordem de serviço.", minRevealReason, maxRevealReason),
		"senha":       fmt.Sprintf("A senha digitada não serve para as máquinas deste lote (até %d caracteres; na Lenovo, sem acento, espaço, vírgula ou ponto e vírgula).", maxCurrentPassword),
		"falta-senha": "Há máquinas neste lote que só entram com a senha atual digitada: o fabricante não tem senha padrão cadastrada, ou a última tentativa falhou. Digite a senha, ou tire essas máquinas do lote.",
		"envelope":    "A senha padrão cadastrada não abriu com a chave de guarda: cadastre de novo em Configuração › BIOS.",
		"mudou":       "A lista mudou desde a conferência (alguma máquina mudou de situação). Confira de novo e repita o pedido.",
		"auditoria":   "A trilha de auditoria não gravou: nada foi enviado.",
	}[q.Get("erro")]
}
