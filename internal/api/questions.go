package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Perguntas ao parque (jobs.TypeQuery): o admin escolhe um sensor do catálogo fechado e o
// argumento; cada máquina do alvo recebe um job "pergunta", e a tela agrupa as respostas.
// Com o canal em tempo real, as máquinas conectadas respondem em segundos. Só admin: o
// sensor lê registro e arquivos como SYSTEM.

const (
	questionTTL      = time.Hour // a pergunta vale por 1 h: quem não respondeu até lá, fica sem resposta
	questionsShown   = 30
	answerMachines   = 200 // máquinas listadas por resposta na tela (o CSV traz todas)
	questionRefresh  = 5   // segundos, enquanto há respostas chegando
	questionLiveFor  = 15 * time.Minute
	targetAll        = "todas"
	maxQuestionArg   = 300
	maxQuestionNames = 128
)

type sensorOption struct {
	Value, Label, Hint string
	Examples           []string
	Selected           bool
}

// sensorGroup: as perguntas de um mesmo assunto, juntas na lista do formulário.
type sensorGroup struct {
	Label   string
	Options []sensorOption
}

var sensorGroups = []sensorGroup{
	{Label: "Sobre a máquina", Options: []sensorOption{
		{Value: jobs.SensorFile, Label: "Arquivo: existe? qual versão?",
			Hint:     "O caminho completo do arquivo. A resposta traz a versão (no Windows), o tamanho, a data e o SHA-256.",
			Examples: []string{`C:\Windows\System32\drivers\x.sys`, `%ProgramFiles%\App\app.exe`, `/usr/bin/ssh`}},
		{Value: jobs.SensorProcess, Label: "Processo: está rodando?", Hint: "O nome do executável.", Examples: []string{"firefox.exe", "sshd"}},
		{Value: jobs.SensorService, Label: "Serviço: rodando ou parado?", Hint: "O nome curto do serviço.", Examples: []string{"Spooler", "ssh"}},
		{Value: jobs.SensorRegistry, Label: "Registro (Windows): qual o valor?", Hint: "A chave, dentro do HKLM; o nome do valor vai no campo de baixo.",
			Examples: []string{`HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`}},
		{Value: jobs.SensorProgram, Label: "Programa instalado: quais versões?", Hint: "Um pedaço do nome, como aparece em Software.",
			Examples: []string{"LibreOffice"}},
	}},
	{Label: "Sobre cada usuário da máquina", Options: []sensorOption{
		{Value: jobs.SensorExtensions, Label: "Extensões dos navegadores: o que cada usuário instalou?",
			Hint: "Deixe vazio para listar todas as extensões de todos os usuários (Chrome, Edge, Firefox, Brave, Opera, Vivaldi), com sessão aberta ou não. " +
				"Para procurar uma só, escreva um pedaço do nome ou o ID dela.",
			Examples: []string{"cjpalhdlnbpafiamejdnhcphjbkeiagm", "tradutor"}},
		{Value: jobs.SensorUserFile, Label: "Arquivo na pasta do usuário: quem tem?",
			Hint:     `O caminho dentro da pasta de cada usuário: começa por ~/ (em qualquer sistema) ou, no Windows, por %USERPROFILE%\, %APPDATA%\ ou %LOCALAPPDATA%\.`,
			Examples: []string{"~/Downloads/instalador.exe", `%APPDATA%\Programa\config.ini`}},
	}},
}

// sensorOptions: todas as perguntas, na ordem da tela.
var sensorOptions = func() []sensorOption {
	var out []sensorOption
	for _, g := range sensorGroups {
		out = append(out, g.Options...)
	}
	return out
}()

func sensorLabel(s string) string {
	for _, o := range sensorOptions {
		if o.Value == s {
			return strings.SplitN(o.Label, ":", 2)[0]
		}
	}
	return s
}

type questionRow struct {
	ID                       int64
	Sensor, Question, Target string
	By, When                 string
	Machines, Answered       int
}

type questionsView struct {
	pageBase
	Rows      []questionRow
	Groups    []sensorGroup
	Sensors   []sensorOption
	Rings     []ringChoice
	CanAsk    bool
	Connected int // máquinas no canal em tempo real agora
	ChannelOn bool
	Arg, Name string
	Target    string
}

type answerGroup struct {
	Value    string
	Count    int
	Pct      int
	Level    string // ok, atencao, erro, muted
	Machines []answerMachine
	More     int
}

type answerMachine struct {
	ID, Name, Extra string
}

type questionView struct {
	pageBase
	Q                      store.Question
	Sensor, Question, When string
	Groups                 []answerGroup
	Answered, Waiting      int
	Live                   bool
	// Perguntas em lista (as que olham cada usuário): o parque juntado por item.
	List       bool
	Extensions bool // colunas de extensão (ID, navegador); senão, as do arquivo
	Totals     store.QuestionItemTotals
	Items      []itemGroupRow
	Text, Sort string
	Sorts      []sortOption
	Found      int    // grupos depois do filtro
	Range      string // "101–200 de 340"
	Prev, Next string
}

type sortOption struct {
	Value, Label string
	Selected     bool
}

// itemGroupRow: um item (uma extensão) e em quantas máquinas e usuários apareceu.
type itemGroupRow struct {
	Key, Name, Note   string
	Versions, Sources string
	Machines, Users   int
	Href              string
}

// itemView: GET /painel/perguntas/{id}/item, quem tem o item.
type itemView struct {
	pageBase
	Q                      store.Question
	Sensor                 string
	Extensions             bool
	Key, Name              string
	Rows                   []itemRow
	Machines, Users        int
	More                   int
	StoreChrome, StoreEdge string // a página da extensão nas lojas, para conferir
	StoreFirefox           string
}

type itemRow struct {
	ID, Machine                        string
	User, Source, Where, Version, Note string
}

const (
	itemGroupsPerPage = 100
	itemRowsShown     = 500
	listRefresh       = 15 // segundos: a tela em lista junta o parque inteiro a cada carga
)

var chromiumExtID = regexp.MustCompile(`^[a-p]{32}$`)

func questionText(q store.Question) string {
	switch {
	case q.Sensor == jobs.SensorRegistry:
		name := q.Name
		if name == "" {
			name = "(padrão)"
		}
		return q.Arg + " → " + name
	case q.Sensor == jobs.SensorExtensions && q.Arg == "":
		return "todas"
	}
	return q.Arg
}

// questionsPage: GET /painel/perguntas.
func (a *api) questionsPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := questionsView{pageBase: a.baseFor(r.Context(), s, "Perguntas ao parque", "campanhas"), CanAsk: a.jobKey != nil,
		ChannelOn: a.channelOn(), Connected: a.channel.Connected(), Target: targetAll}
	v.Sub = "perguntas"
	q := r.URL.Query()
	switch q.Get("erro") {
	case "pergunta":
		v.Error = "Pergunta inválida: " + texto80(q.Get("motivo"))
	case "alvo":
		v.Error = "Escolha todas as máquinas ou um grupo de implantação com máquinas."
	case "agente":
		v.Error = "Nenhuma máquina do alvo tem agente que responda a esta pergunta: ela chegou numa versão mais nova do agente. Atualize os agentes e pergunte de novo."
	}
	sensor := q.Get("sensor")
	for _, g := range sensorGroups {
		group := sensorGroup{Label: g.Label}
		for _, o := range g.Options {
			o.Selected = o.Value == sensor
			group.Options = append(group.Options, o)
			v.Sensors = append(v.Sensors, o)
		}
		v.Groups = append(v.Groups, group)
	}
	v.Arg, v.Name = q.Get("arg"), q.Get("nome")
	var err error
	if v.Rings, err = a.ringChoices(r, nil); err != nil {
		a.internalError(w, r, "listar grupos de implantação", err)
		return
	}
	list, err := a.store.Questions(r.Context(), questionsShown)
	if err != nil {
		a.internalError(w, r, "listar perguntas", err)
		return
	}
	for _, qq := range list {
		v.Rows = append(v.Rows, questionRow{ID: qq.ID, Sensor: sensorLabel(qq.Sensor), Question: questionText(qq), Target: qq.Target,
			By: qq.CreatedBy, When: a.stamp(qq.CreatedAt), Machines: qq.Machines, Answered: qq.Answered})
	}
	a.render(w, r, http.StatusOK, "perguntas", v)
}

// texto80: o motivo do erro que volta pela URL (só para mostrar, encurtado).
func texto80(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}

// askAction: POST /painel/perguntas.
func (a *api) askAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.jobKey == nil {
		http.Error(w, "o servidor não tem a chave de jobs", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	p := jobs.QueryParams{Sensor: r.PostFormValue("sensor"), Arg: strings.TrimSpace(r.PostFormValue("arg")),
		Name: strings.TrimSpace(r.PostFormValue("nome"))}
	if p.Sensor != jobs.SensorRegistry {
		p.Name = ""
	}
	back := url.Values{"sensor": {p.Sensor}, "arg": {texto80(p.Arg)}, "nome": {texto80(p.Name)}}
	if err := p.Validate(); err != nil || len(p.Arg) > maxQuestionArg || len(p.Name) > maxQuestionNames {
		why := "argumento grande demais"
		if err != nil {
			why = err.Error()
		}
		back.Set("erro", "pergunta")
		back.Set("motivo", why)
		http.Redirect(w, r, "/painel/perguntas?"+back.Encode(), http.StatusSeeOther)
		return
	}
	var ids []string
	target := "todas as máquinas"
	switch alvo := r.PostFormValue("alvo"); {
	case alvo == targetAll:
		ms, err := a.store.Machines(r.Context())
		if err != nil {
			a.internalError(w, r, "listar máquinas", err)
			return
		}
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
	default:
		ring, err := strconv.Atoi(alvo)
		if err != nil || ring < 0 || ring > store.MaxRing {
			back.Set("erro", "alvo")
			http.Redirect(w, r, "/painel/perguntas?"+back.Encode(), http.StatusSeeOther)
			return
		}
		if ids, err = a.store.RingMachines(r.Context(), ring); err != nil {
			a.internalError(w, r, "listar o grupo de implantação", err)
			return
		}
		target = fmt.Sprintf("grupo de implantação %d", ring)
	}
	if len(ids) == 0 {
		back.Set("erro", "alvo")
		http.Redirect(w, r, "/painel/perguntas?"+back.Encode(), http.StatusSeeOther)
		return
	}
	// As perguntas por usuário só vão a quem as conhece: o agente anterior recusaria o sensor.
	skipped := 0
	if jobs.ListSensor(p.Sensor) {
		able, err := a.store.MachinesWithCapability(r.Context(), protocol.CapabilityUserQueries)
		if err != nil {
			a.internalError(w, r, "listar os agentes que respondem a pergunta", err)
			return
		}
		all := len(ids)
		ids = slices.DeleteFunc(ids, func(id string) bool { _, ok := slices.BinarySearch(able, id); return !ok })
		if skipped = all - len(ids); len(ids) == 0 {
			back.Set("erro", "agente")
			http.Redirect(w, r, "/painel/perguntas?"+back.Encode(), http.StatusSeeOther)
			return
		}
	}
	raw, err := jsonParams(p)
	if err != nil {
		a.internalError(w, r, "montar a pergunta", err)
		return
	}
	qid, err := a.store.CreateQuestion(r.Context(), store.Question{Sensor: p.Sensor, Arg: p.Arg, Name: p.Name, Target: target,
		Machines: len(ids), CreatedBy: s.Username, Skipped: skipped})
	if err != nil {
		a.internalError(w, r, "gravar a pergunta", err)
		return
	}
	created := 0
	for _, id := range ids {
		_, err := jobs.Issue(r.Context(), a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeQuery, TTL: questionTTL,
			By: s.Username, Params: raw, QuestionID: qid}, a.now())
		switch {
		case errors.Is(err, jobs.ErrMachineUnavailable):
			continue // aposentada no meio
		case err != nil:
			a.internalError(w, r, "criar os jobs da pergunta", err)
			return
		}
		created++
	}
	if created != len(ids) {
		_ = a.store.SetQuestionMachines(r.Context(), qid, created)
	}
	detail := fmt.Sprintf("%s: %s · %s · %d máquina(s)", sensorLabel(p.Sensor), questionText(store.Question{Sensor: p.Sensor, Arg: p.Arg, Name: p.Name}), target, created)
	if skipped > 0 {
		detail += fmt.Sprintf(" · %d de fora (agente anterior à pergunta)", skipped)
	}
	a.audit(r, s, "pergunta.criar", strconv.FormatInt(qid, 10), detail)
	http.Redirect(w, r, "/painel/perguntas/"+strconv.FormatInt(qid, 10), http.StatusSeeOther)
}

// questionAnswers agrupa as respostas da pergunta.
func (a *api) questionAnswers(r *http.Request, id int64) ([]store.QuestionJob, map[string]string, error) {
	list, err := a.store.QuestionJobs(r.Context(), id)
	if err != nil {
		return nil, nil, err
	}
	names, err := a.machineNames(r.Context())
	return list, names, err
}

// answerOf: o valor e o detalhe de um job da pergunta, pelo estado.
func answerOf(j store.QuestionJob) (value, extra, level string) {
	switch j.State {
	case jobs.StatePending, jobs.StateDelivered:
		return "aguardando resposta", "", "muted"
	case jobs.StateExpired:
		return "sem resposta em 1 h (desligada ou fora da rede)", "", "muted"
	case jobs.StateRejected:
		if strings.Contains(j.Detail, "desconhecido") || strings.Contains(j.Detail, "não responde perguntas") {
			return "agente sem as perguntas (atualize o agente)", "", "atencao"
		}
		return "recusado pelo agente", j.Detail, "erro"
	case jobs.StateDone:
		ans := jobs.ParseAnswer(j.Detail)
		return ans.Value, ans.Extra, "ok"
	}
	return j.State, j.Detail, "erro"
}

// questionPage: GET /painel/perguntas/{id}.
func (a *api) questionPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	q, ok, err := a.store.Question(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler a pergunta", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	list, names, err := a.questionAnswers(r, id)
	if err != nil {
		a.internalError(w, r, "ler as respostas", err)
		return
	}
	v := questionView{pageBase: a.baseFor(r.Context(), s, fmt.Sprintf("Pergunta %d", q.ID), "campanhas"), Q: q,
		Sensor: sensorLabel(q.Sensor), Question: questionText(q), When: a.stamp(q.CreatedAt)}
	v.Sub = "perguntas"
	groups := map[string]*answerGroup{}
	for _, j := range list {
		value, extra, level := answerOf(j)
		if level == "muted" && j.State != jobs.StateExpired {
			v.Waiting++
		} else {
			v.Answered++
		}
		g := groups[value]
		if g == nil {
			g = &answerGroup{Value: value, Level: level}
			groups[value] = g
		}
		g.Count++
		if len(g.Machines) < answerMachines {
			g.Machines = append(g.Machines, answerMachine{ID: j.MachineID, Name: dash(names[j.MachineID]), Extra: extra})
		} else {
			g.More++
		}
	}
	for _, g := range groups {
		if len(list) > 0 {
			g.Pct = g.Count * 100 / len(list)
		}
		v.Groups = append(v.Groups, *g)
	}
	slices.SortFunc(v.Groups, func(x, y answerGroup) int {
		if x.Count != y.Count {
			return y.Count - x.Count
		}
		return strings.Compare(x.Value, y.Value)
	})
	v.Live = v.Waiting > 0 && a.now().Sub(q.CreatedAt) < questionLiveFor
	if v.Live {
		v.Refresh = questionRefresh
	}
	if jobs.ListSensor(q.Sensor) {
		if err := a.questionItems(r, &v); err != nil {
			a.internalError(w, r, "ler os itens da pergunta", err)
			return
		}
	}
	a.render(w, r, http.StatusOK, "pergunta", v)
}

// joinShort junta até max textos; o resto vira "+N".
func joinShort(list []string, max int) string {
	if len(list) <= max {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:max], ", ") + fmt.Sprintf(" +%d", len(list)-max)
}

// questionItems preenche a parte em lista da tela: os itens juntados, com filtro, ordem e páginas.
func (a *api) questionItems(r *http.Request, v *questionView) error {
	q := r.URL.Query()
	v.List, v.Extensions = true, v.Q.Sensor == jobs.SensorExtensions
	v.Text = strings.TrimSpace(texto80(q.Get("q")))
	v.Sort = q.Get("ordem")
	for _, o := range []sortOption{{Value: store.ItemSortCommon, Label: "em mais máquinas primeiro"},
		{Value: store.ItemSortRare, Label: "em menos máquinas primeiro (as raras)"}, {Value: store.ItemSortName, Label: "pelo nome"}} {
		o.Selected = o.Value == v.Sort
		v.Sorts = append(v.Sorts, o)
	}
	if !slices.ContainsFunc(v.Sorts, func(o sortOption) bool { return o.Selected }) {
		v.Sort, v.Sorts[0].Selected = store.ItemSortCommon, true
	}
	page, _ := strconv.Atoi(q.Get("pagina"))
	page = min(max(page, 1), 10000)
	var err error
	if v.Totals, err = a.store.QuestionItemTotals(r.Context(), v.Q.ID); err != nil {
		return err
	}
	groups, found, err := a.store.QuestionItemGroups(r.Context(), v.Q.ID, store.QuestionItemQuery{Text: v.Text, Sort: v.Sort,
		Limit: itemGroupsPerPage, Offset: (page - 1) * itemGroupsPerPage})
	if err != nil {
		return err
	}
	if found == 0 && len(groups) == 0 && page > 1 { // a página pedida passou do fim: a contagem vem junto das linhas
		page = 1
		if groups, found, err = a.store.QuestionItemGroups(r.Context(), v.Q.ID, store.QuestionItemQuery{Text: v.Text, Sort: v.Sort,
			Limit: itemGroupsPerPage}); err != nil {
			return err
		}
	}
	v.Found = found
	base := fmt.Sprintf("/painel/perguntas/%d", v.Q.ID)
	for _, g := range groups {
		row := itemGroupRow{Key: g.Key, Name: g.Name, Note: g.Note, Versions: joinShort(g.Versions, 4), Sources: joinShort(g.Sources, 4),
			Machines: g.Machines, Users: g.Users, Href: base + "/item?" + url.Values{"k": {g.Key}}.Encode()}
		v.Items = append(v.Items, row)
	}
	at := func(n int) string {
		u := url.Values{}
		if v.Text != "" {
			u.Set("q", v.Text)
		}
		if v.Sort != store.ItemSortCommon {
			u.Set("ordem", v.Sort)
		}
		if n > 1 {
			u.Set("pagina", strconv.Itoa(n))
		}
		if len(u) == 0 {
			return base
		}
		return base + "?" + u.Encode()
	}
	if found > itemGroupsPerPage {
		from := (page - 1) * itemGroupsPerPage
		v.Range = fmt.Sprintf("%d–%d de %d", from+1, from+len(groups), found)
		if page > 1 {
			v.Prev = at(page - 1)
		}
		if from+len(groups) < found {
			v.Next = at(page + 1)
		}
	}
	if v.Live {
		v.Refresh = listRefresh
	}
	return nil
}

// questionItemPage: GET /painel/perguntas/{id}/item?k=chave, as máquinas e os usuários de um item.
func (a *api) questionItemPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	key := r.URL.Query().Get("k")
	if err != nil || id < 1 || key == "" || len(key) > 400 {
		http.NotFound(w, r)
		return
	}
	q, ok, err := a.store.Question(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler a pergunta", err)
		return
	}
	if !ok || !jobs.ListSensor(q.Sensor) {
		http.NotFound(w, r)
		return
	}
	list, total, err := a.store.QuestionItems(r.Context(), id, key, itemRowsShown)
	if err != nil {
		a.internalError(w, r, "ler os itens da pergunta", err)
		return
	}
	if len(list) == 0 {
		http.NotFound(w, r)
		return
	}
	names, err := a.machineNames(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	v := itemView{pageBase: a.baseFor(r.Context(), s, fmt.Sprintf("Pergunta %d", q.ID), "campanhas"), Q: q, Sensor: sensorLabel(q.Sensor),
		Extensions: q.Sensor == jobs.SensorExtensions, Key: key, More: total - len(list)}
	v.Sub = "perguntas"
	nameCount := map[string]int{}
	machines, users := map[string]bool{}, map[string]bool{}
	for _, it := range list {
		nameCount[it.Name]++
		machines[it.MachineID], users[it.MachineID+"\x00"+it.User] = true, true
		v.Rows = append(v.Rows, itemRow{ID: it.MachineID, Machine: dash(names[it.MachineID]), User: it.User, Source: it.Source, Where: it.Where,
			Version: it.Version, Note: it.Note})
	}
	slices.SortStableFunc(v.Rows, func(x, y itemRow) int { return strings.Compare(strings.ToLower(x.Machine), strings.ToLower(y.Machine)) })
	v.Machines, v.Users = len(machines), len(users)
	for name, n := range nameCount {
		if n > nameCount[v.Name] || n == nameCount[v.Name] && name < v.Name || v.Name == "" {
			v.Name = name
		}
	}
	if v.Extensions {
		// O ID vai num endereço fixo de loja, só depois de conferido: nada do agente vira link livre.
		if chromiumExtID.MatchString(key) {
			v.StoreChrome = "https://chromewebstore.google.com/detail/" + key
			v.StoreEdge = "https://microsoftedge.microsoft.com/addons/detail/" + key
		} else {
			v.StoreFirefox = "https://addons.mozilla.org/firefox/addon/" + url.PathEscape(key) + "/"
		}
	}
	a.render(w, r, http.StatusOK, "pergunta-item", v)
}

// questionCSV: GET /painel/perguntas/{id}/csv, uma linha por máquina.
func (a *api) questionCSV(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	q, ok, err := a.store.Question(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler a pergunta", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	list, names, err := a.questionAnswers(r, id)
	if err != nil {
		a.internalError(w, r, "ler as respostas", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="patchd-pergunta-%d.csv"`, id))
	h.Set("Cache-Control", "no-store")
	if jobs.ListSensor(q.Sensor) {
		a.questionListCSV(w, r, s, q, list, names)
		return
	}
	rows := make([][]string, 0, len(list))
	for _, j := range list {
		value, extra, _ := answerOf(j)
		rows = append(rows, []string{j.MachineID, names[j.MachineID], value, extra, strconv.FormatInt(j.JobID, 10)})
	}
	var b bytes.Buffer
	if err := export.Table(&b, []string{"maquina", "nome", "resposta", "detalhe", "job"}, rows); err != nil {
		h.Del("Content-Disposition")
		a.internalError(w, r, "montar CSV", err)
		return
	}
	a.audit(r, s, "exportar.csv", r.URL.Path, fmt.Sprintf("%d linha(s)", len(rows)))
	_, _ = w.Write(b.Bytes())
}

// questionListCSV: o CSV de uma pergunta em lista, uma linha por item (uma extensão de um
// usuário de uma máquina); a máquina sem item nenhum sai numa linha só, com a resposta dela.
// Vai direto para a resposta, um item por vez: a lista do parque inteiro não cabe na memória
// de uma requisição.
func (a *api) questionListCSV(w http.ResponseWriter, r *http.Request, s panel.Session, q store.Question, list []store.QuestionJob, names map[string]string) {
	header := []string{"maquina", "nome", "usuario", "item", "id", "versao", "navegador", "perfil", "observacao", "resposta_da_maquina"}
	if q.Sensor == jobs.SensorUserFile {
		header = []string{"maquina", "nome", "usuario", "achado", "chave", "versao", "origem", "caminho", "detalhe", "resposta_da_maquina"}
	}
	answers := make(map[string]string, len(list))
	for _, j := range list {
		answers[j.MachineID], _, _ = answerOf(j)
	}
	tw, err := export.NewTable(w, header)
	if err != nil {
		a.logger.Error("CSV da pergunta", "error", err, "request_id", RequestID(r.Context()))
		return
	}
	n := 0
	with := map[string]bool{}
	err = a.store.EachQuestionItem(r.Context(), q.ID, func(it store.QuestionItem) error {
		with[it.MachineID] = true
		n++
		return tw.Row(it.MachineID, names[it.MachineID], it.User, it.Name, it.Key, it.Version, it.Source, it.Where, it.Note, answers[it.MachineID])
	})
	for _, j := range list {
		if err != nil {
			break
		}
		if !with[j.MachineID] {
			value, extra, _ := answerOf(j)
			n++
			err = tw.Row(j.MachineID, names[j.MachineID], "", "", "", "", "", "", extra, value)
		}
	}
	if cerr := tw.Close(); err == nil {
		err = cerr
	}
	if err != nil { // o cabeçalho já saiu: o arquivo fica cortado, e o registro conta por quê
		a.logger.Error("CSV da pergunta cortado", "error", err, "question_id", q.ID, "request_id", RequestID(r.Context()))
	}
	a.audit(r, s, "exportar.csv", r.URL.Path, fmt.Sprintf("%d linha(s)", n))
}

func jsonParams(p jobs.QueryParams) ([]byte, error) { return json.Marshal(p) }
