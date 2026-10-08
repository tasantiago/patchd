package api

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
)

// As telas do painel (Aula 7.6): a frota, o detalhe de uma máquina e as aposentadas.
// Tudo é montado no servidor com html/template, que escapa cada valor conforme o lugar
// (texto, atributo, URL): nenhum nome de máquina ou motivo vira HTML. Sem JavaScript.

const (
	maxItemsShown   = 300 // pendências mostradas no detalhe (a API devolve todas)
	maxReasonLength = 200
	minReasonLength = 3
)

// pageBase é o que toda página tem: a sessão e o menu.
type pageBase struct {
	Title   string
	Session panel.Session
	Admin   bool
	Nav     string // frota ou aposentadas
	Flash   string // mensagem de sucesso (de uma lista fixa)
	Error   string // mensagem de erro (de uma lista fixa)
}

func (a *api) base(r *http.Request, title, nav string) pageBase {
	s, _ := SessionFrom(r.Context())
	return pageBase{Title: title, Session: s, Admin: s.Role == panel.RoleAdmin, Nav: nav}
}

func (a *api) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	pageHeaders(w)
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		a.logger.Error("falha ao montar a página", "page", name, "error", err, "request_id", RequestID(r.Context()))
	}
}

// ago escreve há quanto tempo, como na linha de comando.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// --- Frota -------------------------------------------------------------------------

type countView struct {
	State, Label string
	N            int
	Active       bool
}

type fleetRow struct {
	ID, Short, Name, OS, State, Label, Pending, Seen string
	Exploited                                        int
	Reasons                                          []string
}

type fleetView struct {
	pageBase
	Unavailable string // sem estado de compliance (memória) ou avaliação com erro
	Counts      []countView
	Filter      string
	Total       int
	Rows        []fleetRow
	EvaluatedAt string
}

// fleetPage lista a frota com o estado de cada máquina; ?estado= filtra.
func (a *api) fleetPage(w http.ResponseWriter, r *http.Request) {
	v := fleetView{pageBase: a.base(r, "Frota", "frota")}
	filter := compliance.State(r.URL.Query().Get("estado"))
	if filter.Rank() < len(compliance.States) {
		v.Filter = string(filter)
	}
	if a.compliance == nil {
		v.Unavailable = "O estado de compliance exige o PostgreSQL com o catálogo."
		a.render(w, r, http.StatusOK, "frota", v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	fleet, err := a.compliance.Fleet(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.logger.Error("falha interna", "op", "avaliar a frota", "error", err, "request_id", RequestID(r.Context()))
		v.Unavailable = "A avaliação da frota falhou; veja o log do servidor."
		a.render(w, r, http.StatusOK, "frota", v)
		return
	}
	v.Total, v.EvaluatedAt = len(fleet.Machines), fleet.EvaluatedAt.Format("02/01/2006 15:04")+" UTC"
	for _, st := range compliance.States {
		v.Counts = append(v.Counts, countView{State: string(st), Label: st.Label(), N: fleet.Counts[string(st)], Active: v.Filter == string(st)})
	}
	for _, m := range fleet.Machines {
		if v.Filter != "" && m.State != v.Filter {
			continue
		}
		pend := "—"
		if m.Pending > 0 {
			pend = fmt.Sprintf("%d %s", m.Pending, compliance.Unit(m.Pending, m.PendingUnit))
		}
		v.Rows = append(v.Rows, fleetRow{ID: m.MachineID, Short: short(m.MachineID), Name: dash(m.Hostname), OS: dash(m.OS),
			State: m.State, Label: compliance.State(m.State).Label(), Pending: pend, Exploited: m.Exploited,
			Seen: ago(fleet.EvaluatedAt.Sub(m.LastSeenAt)), Reasons: m.Reasons})
	}
	a.render(w, r, http.StatusOK, "frota", v)
}

// --- Detalhe da máquina ------------------------------------------------------------

type linkView struct {
	ID, Short, Relation string
	Retired             bool
}

type machineView struct {
	pageBase
	D                                    protocol.ComplianceDetail
	Name, Label, Seen, Collected, Reboot string
	Pending                              string
	Links                                []linkView
	Items                                []protocol.PendingItem
	More                                 int
	MinReason, MaxReason                 int
}

func relationLabel(r string) string {
	switch r {
	case "reenrollment":
		return "mesma instalação"
	case "clone":
		return "clone (mesma instalação, outro hardware)"
	case "same_hardware":
		return "mesmo hardware"
	}
	return r
}

// machinePage mostra o estado de uma máquina, as pendências e os alertas de identidade.
func (a *api) machinePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v := machineView{pageBase: a.base(r, "Máquina", "frota"), MinReason: minReasonLength, MaxReason: maxReasonLength}
	if !machineIDPattern.MatchString(id) || a.compliance == nil {
		a.render(w, r, http.StatusNotFound, "nao-encontrada", v.pageBase)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	d, found, err := a.compliance.Detail(ctx, id)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		a.render(w, r, http.StatusNotFound, "nao-encontrada", v.pageBase)
		return
	}
	v.D, v.Name, v.Label = d, dash(d.Hostname), compliance.State(d.State).Label()
	v.Title = "Máquina " + short(d.MachineID)
	if d.Hostname != "" {
		v.Title = d.Hostname
	}
	v.Seen = ago(d.EvaluatedAt.Sub(d.LastSeenAt)) + " (" + d.LastSeenAt.Format("02/01/2006 15:04") + " UTC)"
	v.Collected = "—"
	if d.InventoryCollectedAt != nil {
		v.Collected = d.InventoryCollectedAt.Format("02/01/2006 15:04") + " UTC"
	}
	if d.Pending > 0 {
		v.Pending = fmt.Sprintf("%d %s (%d com exploração conhecida)", d.Pending, compliance.Unit(d.Pending, d.PendingUnit), d.Exploited)
	}
	v.Items = d.Items
	if len(v.Items) > maxItemsShown {
		v.More = len(v.Items) - maxItemsShown
		v.Items = v.Items[:maxItemsShown]
	}
	switch q := r.URL.Query(); {
	case q.Has("restaurada"):
		v.Flash = "Máquina devolvida à frota; a credencial dela voltou a valer."
	case q.Get("erro") == "motivo":
		v.Error = fmt.Sprintf("Informe o motivo da aposentadoria (de %d a %d caracteres).", minReasonLength, maxReasonLength)
	}

	links, err := a.store.IdentityLinks(r.Context())
	if err != nil {
		a.internalError(w, r, "listar alertas de identidade", err)
		return
	}
	retired, err := a.store.RetiredMachines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aposentadas", err)
		return
	}
	isRetired := map[string]bool{}
	for _, m := range retired {
		isRetired[m.ID] = true
	}
	for _, l := range links {
		other := ""
		switch id {
		case l.MachineID:
			other = l.RelatedMachineID
		case l.RelatedMachineID:
			other = l.MachineID
		default:
			continue
		}
		v.Links = append(v.Links, linkView{ID: other, Short: short(other), Relation: relationLabel(l.Relation), Retired: isRetired[other]})
	}
	a.render(w, r, http.StatusOK, "maquina", v)
}

// --- Aposentadas -------------------------------------------------------------------

type retiredRow struct {
	ID, Short, Name, OS, When, By, Reason string
}

type retiredView struct {
	pageBase
	Rows []retiredRow
}

func (a *api) retiredPage(w http.ResponseWriter, r *http.Request) {
	v := retiredView{pageBase: a.base(r, "Máquinas aposentadas", "aposentadas")}
	if q := r.URL.Query(); q.Has("aposentada") {
		v.Flash = "Máquina aposentada: saiu da frota, e a credencial dela deixou de valer."
	}
	list, err := a.store.RetiredMachines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aposentadas", err)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RetiredAt.After(list[j].RetiredAt) })
	for _, m := range list {
		v.Rows = append(v.Rows, retiredRow{ID: m.ID, Short: short(m.ID), Name: dash(m.Hostname),
			OS: dash(strings.TrimSpace(m.OSName + " " + m.OSVersion)), When: m.RetiredAt.Format("02/01/2006 15:04") + " UTC",
			By: dash(m.RetiredBy), Reason: m.Reason})
	}
	a.render(w, r, http.StatusOK, "aposentadas", v)
}

// --- Ações do admin ----------------------------------------------------------------

// adminAction exige sessão com o perfil admin. Leitura recebe 403, e a tentativa vai
// para o log (alguém montou o pedido à mão: o botão nem aparece para esse perfil).
func (a *api) adminAction(next func(http.ResponseWriter, *http.Request, panel.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, _, ok, err := a.currentSession(r)
		if err != nil {
			a.internalError(w, r, "conferir sessão", err)
			return
		}
		if !ok {
			http.Redirect(w, r, "/painel/entrar", http.StatusSeeOther)
			return
		}
		if s.Role != panel.RoleAdmin {
			a.logger.Warn("ação de admin recusada", "username", s.Username, "role", s.Role, "path", r.URL.Path,
				"request_id", RequestID(r.Context()))
			b := pageBase{Title: "Sem permissão", Session: s, Nav: "frota"}
			a.render(w, r, http.StatusForbidden, "proibido", b)
			return
		}
		next(w, r, s)
	}
}

func machinePath(id string) string { return "/painel/maquinas/" + url.PathEscape(id) }

// retireAction aposenta a máquina pelo painel, com o motivo e o usuário registrados.
func (a *api) retireAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("motivo"))
	if n := utf8.RuneCountInString(reason); n < minReasonLength || n > maxReasonLength {
		http.Redirect(w, r, machinePath(id)+"?erro=motivo", http.StatusSeeOther)
		return
	}
	changed, err := a.store.RetireMachine(r.Context(), id, reason, s.Username)
	if err != nil {
		a.internalError(w, r, "aposentar máquina", err)
		return
	}
	if changed {
		a.logger.Info("máquina aposentada pelo painel", "machine_id", id, "username", s.Username, "reason", reason,
			"request_id", RequestID(r.Context()))
	}
	http.Redirect(w, r, "/painel/aposentadas?aposentada=1", http.StatusSeeOther)
}

// restoreAction devolve a máquina à frota.
func (a *api) restoreAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	changed, err := a.store.RestoreMachine(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "restaurar máquina", err)
		return
	}
	if !changed {
		http.Redirect(w, r, "/painel/aposentadas", http.StatusSeeOther)
		return
	}
	a.logger.Info("máquina restaurada pelo painel", "machine_id", id, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, machinePath(id)+"?restaurada=1", http.StatusSeeOther)
}

// --- Modelos -----------------------------------------------------------------------

var pages = template.Must(template.New("").Parse(`
{{define "topo"}}<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · patchd</title>
<style>
:root{--fg:#1b1b1b;--muted:#5f6368;--line:#e2e4e8;--bg:#fff;--soft:#f6f7f9;--link:#0b57d0}
*{box-sizing:border-box}body{margin:0;font:15px/1.45 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
header{display:flex;flex-wrap:wrap;gap:.5rem 1.5rem;align-items:center;padding:.7rem 1.25rem;border-bottom:1px solid var(--line);background:var(--soft)}
header strong{font-size:1.05rem}header nav a{margin-right:1rem;color:var(--fg);text-decoration:none}
header nav a.ativo{font-weight:600;border-bottom:2px solid var(--fg)}.quem{margin-left:auto;color:var(--muted);font-size:.9rem}
.quem form{display:inline}.quem button{margin-left:.6rem}
main{max-width:76rem;margin:0 auto;padding:1.25rem}a{color:var(--link)}h1{font-size:1.35rem;margin:.2rem 0 1rem}
table{border-collapse:collapse;width:100%;font-size:.92rem}th,td{text-align:left;padding:.45rem .5rem;border-bottom:1px solid var(--line);vertical-align:top}
th{font-weight:600;color:var(--muted);font-size:.82rem;text-transform:uppercase;letter-spacing:.02em}
td.num{text-align:right;font-variant-numeric:tabular-nums}code{font-size:.88em}
.estado{display:inline-block;padding:.1rem .5rem;border-radius:999px;font-size:.8rem;font-weight:600;white-space:nowrap;border:1px solid}
.faltando{color:#8a1c12;background:#fdecea;border-color:#f3b8b1}.reboot_pendente{color:#7a4a00;background:#fff4e0;border-color:#f5d08f}
.sem_dado_recente{color:#4a4f57;background:#eef0f3;border-color:#cfd4da}.desconhecido{color:#3d4a66;background:#eef2fb;border-color:#c8d3ec}
.em_dia{color:#165c2b;background:#e8f5ec;border-color:#a9d8b6}
.contagens{display:flex;flex-wrap:wrap;gap:.5rem;margin:0 0 1rem;padding:0;list-style:none}
.contagens a{display:block;padding:.45rem .8rem;border:1px solid var(--line);border-radius:.5rem;text-decoration:none;color:var(--fg)}
.contagens a.ativo{outline:2px solid var(--fg)}.contagens b{font-size:1.15rem;margin-right:.35rem}
.motivos{margin:.2rem 0 0;padding-left:1.1rem;color:var(--muted);font-size:.85rem}
.aviso{padding:.6rem .8rem;border-radius:.4rem;margin:0 0 1rem}.ok{background:#e8f5ec}.erro{background:#fdecea;color:#8a1c12}
dl.dados{display:grid;grid-template-columns:max-content 1fr;gap:.35rem 1.2rem;margin:0 0 1.5rem}dl.dados dt{color:var(--muted)}dl.dados dd{margin:0}
.expl{color:#8a1c12;font-weight:600}.muted{color:var(--muted)}section{margin-top:1.75rem}h2{font-size:1.05rem;margin:0 0 .6rem}
form.acao{margin-top:.6rem;display:flex;flex-wrap:wrap;gap:.5rem;align-items:center}form.acao input{flex:1 1 20rem;padding:.4rem}
button{padding:.35rem .9rem;cursor:pointer}
</style></head><body>
<header><strong>patchd</strong>
<nav><a href="/painel/" {{if eq .Nav "frota"}}class="ativo"{{end}}>Frota</a><a href="/painel/aposentadas" {{if eq .Nav "aposentadas"}}class="ativo"{{end}}>Aposentadas</a></nav>
<span class="quem">{{.Session.Username}} ({{.Session.Source}}) · {{.Session.Role}}
<form method="post" action="/painel/sair"><button type="submit">Sair</button></form></span>
</header><main>
{{if .Flash}}<p class="aviso ok" role="status">{{.Flash}}</p>{{end}}
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{end}}
{{end}}

{{define "fim"}}</main></body></html>{{end}}

{{define "frota"}}{{template "topo" .}}
<h1>Frota</h1>
{{if .Unavailable}}<p class="aviso erro">{{.Unavailable}}</p>{{else}}
<ul class="contagens">
<li><a href="/painel/" {{if not .Filter}}class="ativo"{{end}}><b>{{.Total}}</b>todas</a></li>
{{range .Counts}}<li><a href="/painel/?estado={{.State}}" {{if .Active}}class="ativo"{{end}}><b>{{.N}}</b><span class="estado {{.State}}">{{.Label}}</span></a></li>{{end}}
</ul>
{{if .Rows}}<table>
<thead><tr><th>Estado</th><th>Máquina</th><th>Nome</th><th>SO</th><th>Pendentes</th><th>Exploradas</th><th>Último contato</th></tr></thead>
<tbody>{{range .Rows}}<tr>
<td><span class="estado {{.State}}">{{.Label}}</span></td>
<td><a href="/painel/maquinas/{{.ID}}"><code>{{.Short}}</code></a></td>
<td>{{.Name}}</td><td>{{.OS}}</td><td>{{.Pending}}</td>
<td class="num">{{if .Exploited}}<span class="expl">{{.Exploited}}</span>{{else}}0{{end}}</td><td>{{.Seen}}</td></tr>
{{if .Reasons}}<tr><td></td><td colspan="6"><ul class="motivos">{{range .Reasons}}<li>{{.}}</li>{{end}}</ul></td></tr>{{end}}
{{end}}</tbody></table>
{{else}}<p class="muted">Nenhuma máquina{{if .Filter}} neste estado{{end}}.</p>{{end}}
<p class="muted">Avaliado em {{.EvaluatedAt}} · <a href="/painel/frota.csv{{if .Filter}}?estado={{.Filter}}{{end}}">Exportar CSV</a> · <a href="/api/v1/compliance">JSON</a></p>
{{end}}
{{template "fim"}}{{end}}

{{define "maquina"}}{{template "topo" .}}
<p><a href="/painel/">← Frota</a></p>
<h1>{{.Title}} <span class="estado {{.D.State}}">{{.Label}}</span></h1>
{{if .D.Reasons}}<ul>{{range .D.Reasons}}<li>{{.}}</li>{{end}}</ul>{{end}}
<dl class="dados">
<dt>ID</dt><dd><code>{{.D.MachineID}}</code></dd>
<dt>Nome</dt><dd>{{.Name}}</dd>
<dt>Sistema</dt><dd>{{if .D.OS}}{{.D.OS}}{{else}}—{{end}}</dd>
<dt>Catálogo</dt><dd>{{if .D.Catalog}}{{.D.Catalog}}{{else}}—{{end}}</dd>
<dt>Último contato</dt><dd>{{.Seen}}</dd>
<dt>Inventário coletado</dt><dd>{{.Collected}}</dd>
<dt>Agente</dt><dd>{{if .D.AgentVersion}}{{.D.AgentVersion}}{{else}}—{{end}}</dd>
{{if .Pending}}<dt>Pendentes</dt><dd>{{.Pending}}</dd>{{end}}
{{if .D.Target}}<dt>Resolve tudo</dt><dd>{{.D.Target}}</dd>{{end}}
{{range .D.CatalogWarnings}}<dt>Atenção</dt><dd>{{.}}</dd>{{end}}
</dl>
{{if .Items}}<section><h2>Pendências</h2><table>
<thead><tr><th>Explorada</th><th>Item</th><th>Severidade</th><th>Instalado</th><th>Corrigido em</th><th>Observação</th></tr></thead>
<tbody>{{range .Items}}<tr><td>{{if .Exploited}}<span class="expl">sim</span>{{end}}</td><td><code>{{.ID}}</code></td>
<td>{{.Severity}}</td><td>{{.Installed}}</td><td>{{.FixedIn}}</td><td>{{.Note}}</td></tr>{{end}}</tbody></table>
{{if .More}}<p class="muted">E mais {{.More}}: a lista completa está no CSV.</p>{{end}}
<p class="muted"><a href="/painel/maquinas/{{.D.MachineID}}/pendencias.csv">Exportar pendências (CSV)</a> · <a href="/api/v1/machines/{{.D.MachineID}}/compliance">JSON</a></p>
</section>{{end}}
{{if .Links}}<section><h2>Alertas de identidade</h2><ul>
{{range .Links}}<li>{{.Relation}}: {{if .Retired}}<code>{{.Short}}</code> (aposentada){{else}}<a href="/painel/maquinas/{{.ID}}"><code>{{.Short}}</code></a>{{end}}</li>{{end}}
</ul></section>{{end}}
{{if .Admin}}<section><h2>Aposentar</h2>
<p class="muted">A máquina sai da frota, do compliance e do painel, e a credencial dela deixa de valer. Nada é apagado; dá para restaurar em Aposentadas.</p>
<form class="acao" method="post" action="/painel/maquinas/{{.D.MachineID}}/aposentar">
<input name="motivo" required minlength="{{.MinReason}}" maxlength="{{.MaxReason}}" placeholder="Motivo (ex.: registro antigo da VM reinstalada)">
<button type="submit">Aposentar</button></form></section>{{end}}
{{template "fim"}}{{end}}

{{define "aposentadas"}}{{template "topo" .}}
<h1>Máquinas aposentadas</h1>
{{if .Rows}}<table>
<thead><tr><th>Máquina</th><th>Nome</th><th>SO</th><th>Aposentada em</th><th>Por</th><th>Motivo</th>{{if .Admin}}<th></th>{{end}}</tr></thead>
<tbody>{{range .Rows}}<tr><td><code>{{.Short}}</code></td><td>{{.Name}}</td><td>{{.OS}}</td><td>{{.When}}</td><td>{{.By}}</td><td>{{.Reason}}</td>
{{if $.Admin}}<td><form method="post" action="/painel/maquinas/{{.ID}}/restaurar"><button type="submit">Restaurar</button></form></td>{{end}}</tr>{{end}}
</tbody></table>
{{else}}<p class="muted">Nenhuma máquina aposentada.</p>{{end}}
{{template "fim"}}{{end}}

{{define "nao-encontrada"}}{{template "topo" .}}
<h1>Máquina não encontrada</h1>
<p>Ela não existe, está aposentada, ou o estado de compliance está indisponível. <a href="/painel/">Voltar à frota</a>.</p>
{{template "fim"}}{{end}}

{{define "proibido"}}{{template "topo" .}}
<h1>Sem permissão</h1>
<p>Esta ação é do perfil admin. <a href="/painel/">Voltar à frota</a>.</p>
{{template "fim"}}{{end}}
`))
