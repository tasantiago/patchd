package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
)

// Autoatendimento no painel (Aula 17.7): o que os usuários pediram, como terminou e o que
// foi recusado, em Automação › Autoatendimento. É a visão da TI sobre a loja: quais
// programas saem mais, onde a instalação falha e quem esbarra na oferta.

const (
	ssReportJobs     = 500 // pedidos lidos do período
	ssReportRefusals = 30  // recusas mostradas
	ssReportTop      = 8   // programas no "mais pedidos"
)

var ssReportPeriods = []int{7, 30, 90}

type ssKPI struct {
	Label, Sub, Color, Filter string
	N                         int
	Active                    bool
}

type ssReqRow struct {
	ID                                     int64
	When, User, MachineID, Short, Name     string
	What, State, StateLabel, Detail, Group string
}

type ssRefusalRow struct {
	When, User, MachineID, Short, Detail string
}

type ssTopRow struct {
	Name string
	N    int
}

type ssReportView struct {
	pageBase
	Waiting  []ssWaitingRow // Aula 18.3
	Days     int
	Periods  []int
	Filter   string
	KPIs     []ssKPI
	Users    int
	Rows     []ssReqRow
	Shown    int
	Total    int
	Refusals []ssRefusalRow
	Top      []ssTopRow
}

// ssGroup agrupa os estados do job como a TI lê o resultado de um pedido.
func ssGroup(state string) string {
	switch state {
	case jobs.StatePending, jobs.StateDelivered:
		return "andamento"
	case jobs.StateDone, jobs.StateDismissed:
		return "concluido"
	case jobs.StatePostponed:
		return "adiado"
	default: // falhou, rejeitado, expirado, cancelado
		return "falha"
	}
}

func (a *api) selfServiceReportPage(w http.ResponseWriter, r *http.Request) {
	b := a.base(r, "Autoatendimento", "autoatendimento")
	q := r.URL.Query()
	switch q.Get("msg") { // Aula 18.3: o resultado da decisão
	case "aprovado":
		if n, err := strconv.ParseInt(q.Get("job"), 10, 64); err == nil && n > 0 {
			b.Flash = "Pedido aprovado: job " + strconv.FormatInt(n, 10) + " criado, com o usuário como autor. A máquina recebe no próximo contato."
		}
	case "recusado":
		b.Flash = "Pedido recusado. O usuário vê o motivo em Meus pedidos, na Central de software."
	}
	a.renderSSReport(w, r, http.StatusOK, b)
}

func (a *api) renderSSReport(w http.ResponseWriter, r *http.Request, status int, b pageBase) {
	v := ssReportView{pageBase: b, Days: 30, Periods: ssReportPeriods}
	if d, err := strconv.Atoi(r.URL.Query().Get("dias")); err == nil && slices.Contains(ssReportPeriods, d) {
		v.Days = d
	}
	switch f := r.URL.Query().Get("estado"); f {
	case "andamento", "concluido", "adiado", "falha":
		v.Filter = f
	}
	a.expireJobs(r)
	since := a.now().Add(-time.Duration(v.Days) * 24 * time.Hour)
	list, err := a.store.SelfServiceJobs(r.Context(), since, ssReportJobs)
	if err != nil {
		a.internalError(w, r, "listar pedidos do autoatendimento", err)
		return
	}
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	names := make(map[string]string, len(machines))
	for _, m := range machines {
		names[m.ID] = dash(m.Hostname)
	}
	appNames := map[string]string{}
	if a.tpManifest != nil {
		for _, rule := range a.tpManifest.For("windows") {
			appNames[rule.ID] = rule.Name
		}
	}

	count := map[string]int{}
	users := map[string]bool{}
	top := map[string]int{}
	now := a.now()
	for _, j := range list {
		g := ssGroup(j.State)
		count[g]++
		user := strings.TrimPrefix(j.CreatedBy, store.SelfServiceAuthorPrefix)
		users[strings.ToLower(user)] = true
		app, what := ssWhat(j, appNames)
		if app != "" {
			top[app]++
		}
		if v.Filter != "" && g != v.Filter {
			continue
		}
		name := "—"
		if n, ok := names[j.MachineID]; ok {
			name = n
		}
		v.Rows = append(v.Rows, ssReqRow{ID: j.ID, When: ago(now.Sub(j.CreatedAt)), User: user, MachineID: j.MachineID,
			Short: short(j.MachineID), Name: name, What: what, State: j.State, StateLabel: stateLabel(j.State),
			Detail: dash(j.Detail), Group: g})
	}
	v.Total, v.Users = len(list), len(users)
	kpi := func(label, sub, color, filter string, n int) ssKPI {
		return ssKPI{Label: label, Sub: sub, Color: color, Filter: filter, N: n, Active: v.Filter == filter}
	}
	v.KPIs = []ssKPI{
		kpi("Pedidos atendidos", "viraram job", "", "", len(list)),
		kpi("Em andamento", "na fila ou instalando", "v-cinza", "andamento", count["andamento"]),
		kpi("Concluídos", "inclui reinício não necessário", "v-verde", "concluido", count["concluido"]),
		kpi("Adiados", "programa aberto na hora", "v-laranja", "adiado", count["adiado"]),
		kpi("Não deram certo", "falhou, recusado pelo agente, expirou", "v-vermelho", "falha", count["falha"]),
	}

	ev, err := a.store.AuditEvents(r.Context(), store.AuditFilter{Action: "autoatendimento.recusado", Since: since, Limit: ssReportRefusals})
	if err != nil {
		a.internalError(w, r, "listar recusas do autoatendimento", err)
		return
	}
	for _, e := range ev {
		v.Refusals = append(v.Refusals, ssRefusalRow{When: ago(now.Sub(e.At)), User: e.Username, MachineID: e.Target,
			Short: short(e.Target), Detail: e.Detail})
	}
	for n, c := range top {
		v.Top = append(v.Top, ssTopRow{Name: n, N: c})
	}
	slices.SortFunc(v.Top, func(x, y ssTopRow) int { return cmp.Or(cmp.Compare(y.N, x.N), strings.Compare(x.Name, y.Name)) })
	if len(v.Top) > ssReportTop {
		v.Top = v.Top[:ssReportTop]
	}
	v.Shown = len(v.Rows)

	// Aula 18.3: os pedidos que esperam a TI, de todas as máquinas.
	if _, err := a.store.ExpireSelfServiceRequests(r.Context(), now.Add(-store.RequestTTL), now); err != nil {
		a.internalError(w, r, "expirar pedidos do autoatendimento", err)
		return
	}
	waiting, err := a.store.SelfServiceRequests(r.Context(), store.RequestFilter{Waiting: true, Limit: ssReportWaiting})
	if err != nil {
		a.internalError(w, r, "listar pedidos que esperam a TI", err)
		return
	}
	for _, q := range waiting {
		name := appNames[q.AppID]
		if name == "" {
			name = q.AppID
		}
		mname := "—"
		if n, ok := names[q.MachineID]; ok {
			mname = n
		}
		v.Waiting = append(v.Waiting, ssWaitingRow{ID: q.ID, When: ago(now.Sub(q.CreatedAt)), User: q.Account, MachineID: q.MachineID,
			Short: short(q.MachineID), Name: mname, What: name + " " + q.Version, Action: q.Action,
			Expires: untilText(q.CreatedAt.Add(store.RequestTTL).Sub(now))})
	}
	v.KPIs = append([]ssKPI{{Label: "Aguardando a TI", Sub: "pedidos com aprovação", Color: "v-laranja", Filter: "", N: len(v.Waiting)}}, v.KPIs...)
	v.KPIs[0].Filter = "#aprovacao"
	a.render(w, r, status, "autoatendimento-painel", v)
}

// ssReportWaiting: pedidos aguardando mostrados de uma vez.
const ssReportWaiting = 100

type ssWaitingRow struct {
	ID                                       int64
	When, User, MachineID, Short, Name, What string
	Action, Expires                          string
}

func (a *api) ssRequestID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// approveSelfServiceAction aprova um pedido (admin). Sem dar certo, a página volta com o
// motivo, e o pedido continua aguardando.
func (a *api) approveSelfServiceAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, ok := a.ssRequestID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	res, why, err := a.selfService().Approve(r.Context(), id, s.Username)
	switch {
	case errors.Is(err, selfservice.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, selfservice.ErrOff):
		why = "o autoatendimento está desligado neste servidor (sem a chave de jobs ou sem as regras de terceiros)"
	case err != nil:
		a.internalError(w, r, "aprovar pedido do autoatendimento", err)
		return
	}
	if why != "" {
		b := a.baseFor(r.Context(), s, "Autoatendimento", "autoatendimento")
		b.Error = fmt.Sprintf("Pedido nº %d não aprovado: %s", id, why)
		a.renderSSReport(w, r, http.StatusUnprocessableEntity, b)
		return
	}
	q := res.Request
	a.audit(r, s, "autoatendimento.aprovar", q.MachineID,
		fmt.Sprintf("pedido %d · job %d · %s %s · %s · de %s", q.ID, res.Job.ID, q.AppID, q.Version, q.Action, q.Account))
	a.logger.Info("autoatendimento: pedido aprovado", "pedido", q.ID, "job_id", res.Job.ID, "machine_id", q.MachineID, "app", q.AppID,
		"username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("/painel/autoatendimento?msg=aprovado&job=%d", res.Job.ID), http.StatusSeeOther)
}

// refuseSelfServiceAction recusa um pedido (admin), com o motivo.
func (a *api) refuseSelfServiceAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, ok := a.ssRequestID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q, why, err := a.selfService().Refuse(r.Context(), id, s.Username, r.PostFormValue("motivo"))
	switch {
	case errors.Is(err, selfservice.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		a.internalError(w, r, "recusar pedido do autoatendimento", err)
		return
	}
	if why != "" {
		b := a.baseFor(r.Context(), s, "Autoatendimento", "autoatendimento")
		b.Error = fmt.Sprintf("Pedido nº %d: %s", id, why)
		a.renderSSReport(w, r, http.StatusUnprocessableEntity, b)
		return
	}
	a.audit(r, s, "autoatendimento.reprovar", q.MachineID,
		fmt.Sprintf("pedido %d · %s %s · %s · de %s · motivo: %s", q.ID, q.AppID, q.Version, q.Action, q.Account, dash(q.Note)))
	http.Redirect(w, r, "/painel/autoatendimento?msg=recusado", http.StatusSeeOther)
}

// ssWhat descreve o pedido pelos parâmetros assinados: o programa ("7-Zip"; vazio no
// reinício) e o texto da linha ("7-Zip 26.04", "reinício às 18:00").
func ssWhat(j store.JobRow, appNames map[string]string) (app, what string) {
	var payload jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	switch j.Type {
	case jobs.TypeThirdParty:
		p, err := jobs.ParseThirdPartyParams(payload.Params)
		if err != nil {
			return "", "programa"
		}
		name := appNames[p.App]
		if name == "" {
			name = p.App
		}
		return name, name + " " + p.Version
	case jobs.TypeReboot:
		p, err := jobs.ParseRebootParams(payload.Params)
		if err == nil && p.At != nil {
			return "", "reinício às " + p.At.Local().Format("15:04")
		}
		return "", "reinício agora"
	}
	return "", typeLabel(j.Type)
}

var _ = template.Must(pages.New("autoatendimento-painel").Parse(`{{template "topo" .}}
<h1>Automação <span class="muted">› Autoatendimento</span></h1>
{{template "automacao-abas" "autoatendimento"}}
<p class="muted">O que os usuários pediram pela Central de software. A oferta (quem pode pedir o quê) fica em Configuração › Autoatendimento.
Período: {{range .Periods}}{{if eq . $.Days}}<b>{{.}} dias</b>{{else}}<a href="?dias={{.}}{{if $.Filter}}&amp;estado={{$.Filter}}{{end}}">{{.}} dias</a>{{end}} {{end}}</p>
<ul class="kpis">{{range .KPIs}}<li><a class="kpi{{if .Active}} ativo{{end}}" href="{{if eq .Filter "#aprovacao"}}#aprovacao{{else}}?dias={{$.Days}}{{if .Filter}}&amp;estado={{.Filter}}{{end}}{{end}}"><span class="kpi-rot">{{.Label}}</span><span class="kpi-val {{.Color}}">{{.N}}</span><span class="kpi-sub">{{.Sub}}</span></a></li>{{end}}
<li><div class="kpi"><span class="kpi-rot">Recusados</span><span class="kpi-val v-cinza">{{len .Refusals}}{{if ge (len .Refusals) 30}}+{{end}}</span><span class="kpi-sub">pedidos fora da oferta ou da regra</span></div></li>
<li><div class="kpi"><span class="kpi-rot">Usuários</span><span class="kpi-val">{{.Users}}</span><span class="kpi-sub">com algum pedido atendido</span></div></li></ul>
{{if .Waiting}}<style>.decisao{display:flex;flex-wrap:wrap;gap:.4rem;align-items:center}.decisao form{display:flex;gap:.3rem;margin:0}.decisao input{width:13rem}</style>
<div class="card" id="aprovacao"><div class="card-cab"><h2>Aguardando a TI</h2><span class="muted">pedidos de programas com aprovação; sem decisão em 7 dias, expiram</span></div>
<div class="tabela"><table><thead><tr><th>Pedido</th><th>Quando</th><th>Usuário</th><th>Máquina</th><th>Nome</th><th>Programa</th><th>Ação</th><th>Expira em</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Waiting}}<tr><td class="num">{{.ID}}</td><td>{{.When}}</td><td>{{.User}}</td><td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td><td>{{.What}}</td><td>{{.Action}}</td><td>{{.Expires}}</td>
{{if $.Admin}}<td><div class="decisao"><form method="post" action="/painel/autoatendimento/pedidos/{{.ID}}/aprovar"><button type="submit">Aprovar</button></form>
<form method="post" action="/painel/autoatendimento/pedidos/{{.ID}}/recusar"><input type="text" name="motivo" maxlength="200" placeholder="motivo (o usuário vê)" aria-label="motivo da recusa do pedido {{.ID}}"><button class="perigo" type="submit">Recusar</button></form></div></td>{{end}}</tr>{{end}}</tbody></table></div></div>{{end}}
<div class="card"><div class="card-cab"><h2>Pedidos{{if .Filter}} · {{range .KPIs}}{{if .Active}}{{.Label}}{{end}}{{end}}{{end}}</h2><span class="muted">{{.Shown}} de {{.Total}} nos últimos {{.Days}} dias{{if ge .Total 500}} (os 500 mais recentes){{end}}</span></div>
{{if .Rows}}<div class="tabela"><table>
<thead><tr><th>Job</th><th>Quando</th><th>Usuário</th><th>Máquina</th><th>Nome</th><th>Pedido</th><th>Estado</th><th>Detalhe</th></tr></thead>
<tbody>{{range .Rows}}<tr><td class="num">{{.ID}}</td><td>{{.When}}</td><td>{{.User}}</td><td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Name}}</td>
<td>{{.What}}</td><td><span class="job {{.State}}">{{.StateLabel}}</span></td><td>{{.Detail}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhum pedido{{if .Filter}} neste estado{{end}} no período.</p>{{end}}</div>
<div class="duas-col">
<div class="card"><div class="card-cab"><h2>Mais pedidos</h2><span class="muted">programas, no período</span></div>
{{if .Top}}<div class="tabela"><table><thead><tr><th>Programa</th><th class="num">Pedidos</th></tr></thead>
<tbody>{{range .Top}}<tr><td>{{.Name}}</td><td class="num">{{.N}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhum programa pedido no período.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Recusas recentes</h2><span class="muted">da auditoria; o motivo completo está no log</span></div>
{{if .Refusals}}<div class="tabela"><table><thead><tr><th>Quando</th><th>Usuário</th><th>Máquina</th><th>Detalhe</th></tr></thead>
<tbody>{{range .Refusals}}<tr><td>{{.When}}</td><td>{{.User}}</td><td><a href="/painel/maquinas/{{.MachineID}}"><code>{{.Short}}</code></a></td><td>{{.Detail}}</td></tr>{{end}}</tbody></table></div>
{{else}}<p class="muted">Nenhuma recusa no período.</p>{{end}}</div>
</div>
{{template "fim"}}`))
