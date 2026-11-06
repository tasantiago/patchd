package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
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
	Selected           bool
}

var sensorOptions = []sensorOption{
	{Value: jobs.SensorFile, Label: "Arquivo: existe? qual versão?", Hint: `C:\Windows\System32\drivers\x.sys ou %ProgramFiles%\App\app.exe`},
	{Value: jobs.SensorProcess, Label: "Processo: está rodando?", Hint: "firefox.exe (no Linux: sshd)"},
	{Value: jobs.SensorService, Label: "Serviço: rodando ou parado?", Hint: "Spooler (no Linux: ssh)"},
	{Value: jobs.SensorRegistry, Label: "Registro (Windows): qual o valor?", Hint: `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`},
	{Value: jobs.SensorProgram, Label: "Programa instalado: quais versões?", Hint: "um pedaço do nome: LibreOffice"},
}

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
}

func questionText(q store.Question) string {
	if q.Sensor == jobs.SensorRegistry {
		name := q.Name
		if name == "" {
			name = "(padrão)"
		}
		return q.Arg + " → " + name
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
	}
	sensor := q.Get("sensor")
	for _, o := range sensorOptions {
		o.Selected = o.Value == sensor
		v.Sensors = append(v.Sensors, o)
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
	raw, err := jsonParams(p)
	if err != nil {
		a.internalError(w, r, "montar a pergunta", err)
		return
	}
	qid, err := a.store.CreateQuestion(r.Context(), store.Question{Sensor: p.Sensor, Arg: p.Arg, Name: p.Name, Target: target,
		Machines: len(ids), CreatedBy: s.Username})
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
	a.audit(r, s, "pergunta.criar", strconv.FormatInt(qid, 10),
		fmt.Sprintf("%s: %s · %s · %d máquina(s)", sensorLabel(p.Sensor), questionText(store.Question{Sensor: p.Sensor, Arg: p.Arg, Name: p.Name}), target, created))
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
	a.render(w, r, http.StatusOK, "pergunta", v)
}

// questionCSV: GET /painel/perguntas/{id}/csv, uma linha por máquina.
func (a *api) questionCSV(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if _, ok, err := a.store.Question(r.Context(), id); err != nil || !ok {
		if err != nil {
			a.internalError(w, r, "ler a pergunta", err)
			return
		}
		http.NotFound(w, r)
		return
	}
	list, names, err := a.questionAnswers(r, id)
	if err != nil {
		a.internalError(w, r, "ler as respostas", err)
		return
	}
	rows := make([][]string, 0, len(list))
	for _, j := range list {
		value, extra, _ := answerOf(j)
		rows = append(rows, []string{j.MachineID, names[j.MachineID], value, extra, strconv.FormatInt(j.JobID, 10)})
	}
	var b bytes.Buffer
	if err := export.Table(&b, []string{"maquina", "nome", "resposta", "detalhe", "job"}, rows); err != nil {
		a.internalError(w, r, "montar CSV", err)
		return
	}
	a.audit(r, s, "exportar.csv", r.URL.Path, fmt.Sprintf("%d linha(s)", len(rows)))
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="patchd-pergunta-%d.csv"`, id))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(b.Bytes())
}

func jsonParams(p jobs.QueryParams) ([]byte, error) { return json.Marshal(p) }
