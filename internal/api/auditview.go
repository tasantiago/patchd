package api

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// Configuração › Auditoria (Aula 15.14): a trilha da 15.13 no painel, só para admin. Filtro
// por usuário, por ação (ou grupo de ações) e por período; 50 por página, do mais novo
// para o mais antigo; CSV com o mesmo filtro, que também entra na trilha.

const (
	auditPerPage = 50
	auditCSVMax  = 20000 // um CSV de auditoria maior que isso pede um filtro
)

// auditPeriods: os períodos oferecidos; a chave vai na URL (?periodo=).
var auditPeriods = []struct {
	Key, Label string
	D          time.Duration
}{
	{"24h", "últimas 24 h", 24 * time.Hour}, {"7d", "últimos 7 dias", 7 * 24 * time.Hour},
	{"30d", "últimos 30 dias", 30 * 24 * time.Hour}, {"90d", "últimos 90 dias", 90 * 24 * time.Hour},
	{"tudo", "tudo", 0},
}

// auditGroups: os grupos de ações, pelo prefixo.
var auditGroups = []struct{ Key, Label string }{
	{"login.", "Logins"}, {"sessao.", "Sessões"}, {"patch.", "Aprovações do sistema"}, {"terceiros.", "Aprovações de terceiros"},
	{"job.", "Jobs"}, {"campanha.", "Campanhas"}, {"maquina.", "Máquinas"}, {"janela.", "Janelas"},
	{"usuario.", "Usuários locais"}, {"token.", "Tokens"}, {"exportar.", "Exportações"}, {"acesso.", "Acessos negados"},
	{"autoatendimento.", "Autoatendimento"}, {"cli.", "Linha de comando"}, {"auditoria.", "Retenção da auditoria"},
	{"notificacao.", "Notificações"}, {"aparencia.", "Aparência"},
	{"parametro.", "Parâmetros"},
}

type auditQuery struct {
	User, Action, Period string
	Before               int64
}

func parseAuditQuery(v url.Values) auditQuery {
	q := auditQuery{User: panel.NormalizeUsername(v.Get("usuario")), Period: "7d"}
	q.User = texto.Clip(q.User, 64) // Aula 20.3: vai para o banco; UTF-8 inválido virava 500
	act := v.Get("acao")
	for _, g := range auditGroups {
		if g.Key == act {
			q.Action = act
		}
	}
	for _, x := range auditActions {
		if x.Key == act {
			q.Action = act
		}
	}
	for _, p := range auditPeriods {
		if p.Key == v.Get("periodo") {
			q.Period = p.Key
		}
	}
	if n, err := strconv.ParseInt(v.Get("antes"), 10, 64); err == nil && n > 0 {
		q.Before = n
	}
	return q
}

func (q auditQuery) values() url.Values {
	v := url.Values{"aba": {"auditoria"}}
	if q.User != "" {
		v.Set("usuario", q.User)
	}
	if q.Action != "" {
		v.Set("acao", q.Action)
	}
	if q.Period != "7d" {
		v.Set("periodo", q.Period)
	}
	if q.Before > 0 {
		v.Set("antes", strconv.FormatInt(q.Before, 10))
	}
	return v
}

func (q auditQuery) filter(now time.Time) store.AuditFilter {
	f := store.AuditFilter{Username: q.User, Action: q.Action, BeforeID: q.Before}
	for _, p := range auditPeriods {
		if p.Key == q.Period && p.D > 0 {
			f.Since = now.Add(-p.D)
		}
	}
	return f
}

type auditRow struct {
	When, User, Source, Action, Target, Detail, Remote, RequestID string
	Denied                                                        bool // recusas e bloqueios, em destaque
}

type auditView struct {
	Q                auditQuery
	Periods, Actions []optView
	Groups           []optView
	Rows             []auditRow
	Next, First, CSV string
}

// auditTab monta a aba.
func (a *api) auditTab(r *http.Request, v *settingsView) error {
	q := parseAuditQuery(r.URL.Query())
	av := &auditView{Q: q}
	for _, p := range auditPeriods {
		av.Periods = append(av.Periods, optView{Value: p.Key, Label: p.Label, Selected: p.Key == q.Period})
	}
	for _, g := range auditGroups {
		av.Groups = append(av.Groups, optView{Value: g.Key, Label: g.Label, Selected: g.Key == q.Action})
	}
	for _, x := range auditActions {
		av.Actions = append(av.Actions, optView{Value: x.Key, Label: x.Label, Selected: x.Key == q.Action})
	}
	f := q.filter(a.now())
	f.Limit = auditPerPage + 1 // um a mais: diz se há página seguinte
	list, err := a.store.AuditEvents(r.Context(), f)
	if err != nil {
		return err
	}
	if len(list) > auditPerPage {
		list = list[:auditPerPage]
		next := q
		next.Before = list[len(list)-1].ID
		av.Next = "/painel/configuracao?" + next.values().Encode()
	}
	if q.Before > 0 {
		first := q
		first.Before = 0
		av.First = "/painel/configuracao?" + first.values().Encode()
	}
	csvQ := q
	csvQ.Before = 0
	cv := csvQ.values()
	cv.Del("aba")
	av.CSV = "/painel/configuracao/auditoria.csv"
	if len(cv) > 0 {
		av.CSV += "?" + cv.Encode()
	}
	for _, e := range list {
		av.Rows = append(av.Rows, auditRow{When: a.stamp(e.At), User: e.Username, Source: dash(panel.SourceLabel(e.Source)),
			Action: AuditLabel(e.Action), Target: dash(e.Target), Detail: e.Detail, Remote: dash(e.Remote), RequestID: e.RequestID,
			Denied: e.Action == "login.recusado" || e.Action == "login.bloqueado" || e.Action == "acesso.negado"})
	}
	v.Audit = av
	return nil
}

// auditCSV: GET /painel/configuracao/auditoria.csv, com o mesmo filtro da aba (sem a
// página): todos os eventos do filtro, até auditCSVMax.
func (a *api) auditCSV(w http.ResponseWriter, r *http.Request, s panel.Session) {
	q := parseAuditQuery(r.URL.Query())
	f := q.filter(a.now())
	f.BeforeID, f.Limit = 0, store.MaxAuditPage
	var all []store.AuditEvent
	for {
		page, err := a.store.AuditEvents(r.Context(), f)
		if err != nil {
			a.internalError(w, r, "ler a trilha de auditoria", err)
			return
		}
		all = append(all, page...)
		if len(page) < f.Limit || len(all) >= auditCSVMax {
			break
		}
		f.BeforeID = page[len(page)-1].ID
	}
	if len(all) > auditCSVMax {
		all = all[:auditCSVMax]
	}
	out := make([][]string, 0, len(all))
	for _, e := range all {
		out = append(out, []string{strconv.FormatInt(e.ID, 10), export.When(e.At), e.Username, e.Source, e.Action, AuditLabel(e.Action),
			e.Target, e.Detail, e.Remote, e.RequestID})
	}
	name := "patchd-auditoria-" + a.now().UTC().Format("20060102-1504") + ".csv"
	// O sendCSV registra a exportação na trilha com a sessão do contexto.
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, s))
	a.sendCSV(w, r, name, len(out), func(b *bytes.Buffer) error {
		return export.Table(b, []string{"id", "quando_utc", "usuario", "origem", "acao", "acao_rotulo", "alvo", "detalhe", "remoto", "request_id"}, out)
	})
}
