package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
)

// Parâmetros pela tela (Aula 19.2): Configuração › Parâmetros. A tela vence; o .env é o valor
// inicial e o "restaurar o padrão". Só admin muda; cada mudança vai para a auditoria com o
// antes e o depois e, com as notificações ligadas, por e-mail para a equipe.

// Settings é o serviço de parâmetros (internal/settings).
type Settings interface {
	Value(key string) string
	Groups() []settings.Group
	Apply(ctx context.Context, values map[string]string, by string) ([]settings.Change, error)
	Reset(ctx context.Context, key, by string) (settings.Change, bool, error)
}

// WithSettings liga a aba Parâmetros e faz o nome do painel, o fuso e o anúncio do cache
// valerem pelo que a tela gravou.
func WithSettings(s Settings) Option {
	return func(a *api) { a.params = s }
}

// WithRepoCacheLive: o anúncio do cache calculado a cada check-in (a URL muda pela tela).
func WithRepoCacheLive(f func() *protocol.RepoCache) Option {
	return func(a *api) { a.repoCacheFn = f }
}

// brandName: o nome do painel que vale agora.
func (a *api) brandName() string {
	if a.params != nil {
		if v := a.params.Value("PATCHD_PANEL_TITLE"); v != "" {
			return v
		}
	}
	return a.brand
}

// appName (Aula 20.10): o nome da Central de software que vale agora.
func (a *api) appName() string {
	if a.params != nil {
		if v := a.params.Value("PATCHD_APP_NAME"); protocol.CheckAppName(v) == nil {
			return v
		}
	}
	return protocol.DefaultAppName
}

// tz: o fuso do painel que vale agora ("" = UTC).
func (a *api) tz() string {
	if a.params != nil {
		return a.params.Value("PATCHD_TIMEZONE")
	}
	return a.timezone
}

// repoAnnouncement: o anúncio do cache para o check-in (nil: nenhum).
func (a *api) repoAnnouncement() *protocol.RepoCache {
	if a.repoCacheFn != nil {
		return a.repoCacheFn()
	}
	return a.repoCache
}

// paramOption é uma opção de um campo de escolha.
type paramOption struct {
	Value, Label string
	Selected     bool
}

// paramField é um parâmetro como a aba mostra.
type paramField struct {
	Key, Label, Help, Unit, Effect string
	Kind                           string // text, number, select
	Value                          string // o valor no campo (o que vale, ou o que foi digitado)
	Options                        []paramOption
	Show                           string // o que vale, como a tela mostra
	Source                         string // tela, .env, padrão
	Who                            string // tela: quem mudou e quando
	EnvShow                        string // o que vale sem a tela
	EnvSource                      string
	CanReset                       bool
	Locked                         string
	Problem                        string
}

type paramGroup struct {
	Name   string
	Fields []paramField
}

// paramsView monta a aba. typed e problems vêm de um envio recusado (o que foi digitado volta).
func (a *api) paramsView(typed map[string]string, problems map[string]string) []paramGroup {
	var out []paramGroup
	for _, g := range a.params.Groups() {
		pg := paramGroup{Name: g.Name}
		for _, f := range g.Fields {
			v := paramField{Key: f.Key, Label: f.Label, Help: f.Help, Unit: f.Unit, Effect: f.Effect, Value: f.Value,
				Show: f.ShowValue, Source: string(f.Source), EnvShow: f.ShowEnv, EnvSource: string(f.EnvSource),
				CanReset: f.Source == settings.FromScreen, Locked: f.LockedWhy, Problem: problems[f.Key]}
			if t, ok := typed[f.Key]; ok {
				v.Value = t
			}
			if f.Source == settings.FromScreen {
				v.Who = fmt.Sprintf("%s em %s", f.UpdatedBy, a.stamp(f.UpdatedAt))
			}
			switch f.Kind {
			case settings.Number:
				v.Kind = "number"
			case settings.Multi: // Aula 19.3: caixas; o valor é a lista separada por vírgula
				v.Kind = "multi"
				on := strings.Split(v.Value, ",")
				for _, o := range f.Options {
					v.Options = append(v.Options, paramOption{Value: o.Value, Label: o.Label, Selected: slices.Contains(on, o.Value)})
				}
			case settings.Select:
				v.Kind = "select"
				found := false
				for _, o := range f.Options {
					sel := o.Value == v.Value
					found = found || sel
					v.Options = append(v.Options, paramOption{Value: o.Value, Label: o.Label, Selected: sel})
				}
				if !found { // um valor do .env fora da lista continua escolhível
					v.Options = append(v.Options, paramOption{Value: v.Value, Label: v.Value, Selected: true})
				}
			default:
				v.Kind = "text"
			}
			pg.Fields = append(pg.Fields, v)
		}
		out = append(out, pg)
	}
	return out
}

// paramLabels: os rótulos das chaves, na ordem da tela (só as conhecidas: a URL não vira texto
// livre na página).
func (a *api) paramLabels(keys []string) []string {
	var out []string
	for _, g := range a.params.Groups() {
		for _, f := range g.Fields {
			if slices.Contains(keys, f.Key) {
				out = append(out, f.Label)
			}
		}
	}
	return out
}

// paramsFlash: o resultado, na volta para a aba.
func (a *api) paramsFlash(q url.Values, b *pageBase) {
	switch {
	case q.Has("gravado"):
		if l := a.paramLabels(strings.Split(q.Get("gravado"), ",")); len(l) > 0 {
			b.Flash = "Gravado: " + strings.Join(l, ", ") + ". O antes e o depois estão na auditoria."
		}
	case q.Get("nada") == "1":
		b.Flash = "Nada mudou: os valores já eram esses."
	case q.Has("restaurado"):
		if l := a.paramLabels([]string{q.Get("restaurado")}); len(l) > 0 {
			b.Flash = "Restaurado: " + l[0] + " volta a seguir o .env."
		}
	}
	if n, err := strconv.Atoi(q.Get("encerradas")); err == nil && n > 0 && b.Flash != "" {
		b.Flash += fmt.Sprintf(" %d sessão(ões) aberta(s) pelo AD ou pelo RHBK encerrada(s): quem saiu da lista não fica com o perfil até a sessão vencer; quem continua entra de novo (a sua continua).", n)
	}
}

// paramsAction: POST /painel/configuracao/parametros, só admin. Grava o cartão enviado: tudo
// ou nada; só o que mudou vai para a auditoria.
func (a *api) paramsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.params == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	group := r.PostForm.Get("grupo")
	values := map[string]string{}
	for _, g := range a.params.Groups() {
		if g.Name != group {
			continue
		}
		for _, f := range g.Fields {
			switch {
			case f.LockedWhy != "" || !r.PostForm.Has(f.Key):
			case f.Kind == settings.Multi: // o campo escondido vazio vem sempre; as caixas, as marcadas
				values[f.Key] = strings.Join(slices.DeleteFunc(slices.Clone(r.PostForm[f.Key]), func(x string) bool { return x == "" }), ",")
			default:
				values[f.Key] = r.PostForm.Get(f.Key)
			}
		}
	}
	if len(values) == 0 {
		http.Redirect(w, r, "/painel/configuracao?aba=parametros&nada=1", http.StatusSeeOther)
		return
	}
	who := s.Username
	before, sources := a.listsBefore(group)
	changes, err := a.params.Apply(r.Context(), values, who)
	var fe settings.FieldErrors
	var rej *settings.RejectedError
	switch {
	case errors.As(err, &fe):
		a.renderParams(w, r, s, values, fe, "Nada foi gravado: corrija os campos marcados.")
		return
	case errors.As(err, &rej):
		a.renderParams(w, r, s, values, nil, "Nada foi gravado: "+shortError(rej.Error(), 300)+".")
		return
	case errors.Is(err, settings.ErrUnknown):
		http.Error(w, "parâmetro desconhecido", http.StatusBadRequest)
		return
	case err != nil:
		a.internalError(w, r, "gravar os parâmetros", err)
		return
	}
	if len(changes) == 0 {
		http.Redirect(w, r, "/painel/configuracao?aba=parametros&nada=1", http.StatusSeeOther)
		return
	}
	keys := make([]string, len(changes))
	lines := make([]string, len(changes))
	for i, c := range changes {
		keys[i], lines[i] = c.Key, c.String()
		a.audit(r, s, "parametro.alterar", c.Key, c.Before+" → "+c.After)
		a.logger.Info("parâmetro alterado pela tela", "parametro", c.Key, "username", s.Username, "request_id", RequestID(r.Context()))
	}
	a.paramsChanged(s, lines)
	ended := a.narrowed(r, s, keys, before, sources)
	http.Redirect(w, r, "/painel/configuracao?aba=parametros&gravado="+url.QueryEscape(strings.Join(keys, ","))+ended, http.StatusSeeOther)
}

// paramsResetAction: POST /painel/configuracao/parametros/restaurar, só admin.
func (a *api) paramsResetAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.params == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	key := r.PostForm.Get("parametro")
	before, sources := a.listsBefore("")
	c, ok, err := a.params.Reset(r.Context(), key, s.Username)
	var rej *settings.RejectedError
	switch {
	case errors.Is(err, settings.ErrUnknown):
		http.Error(w, "parâmetro desconhecido", http.StatusBadRequest)
		return
	case errors.As(err, &rej):
		a.renderParams(w, r, s, nil, nil, "Não restaurado: "+shortError(err.Error(), 300)+".")
		return
	case err != nil:
		a.internalError(w, r, "restaurar o parâmetro", err)
		return
	case !ok:
		http.Redirect(w, r, "/painel/configuracao?aba=parametros&nada=1", http.StatusSeeOther)
		return
	}
	a.audit(r, s, "parametro.restaurar", c.Key, c.Before+" → "+c.After+" (.env)")
	a.logger.Info("parâmetro restaurado para o .env", "parametro", c.Key, "username", s.Username, "request_id", RequestID(r.Context()))
	a.paramsChanged(s, []string{c.String() + " (restaurado para o .env)"})
	ended := a.narrowed(r, s, []string{c.Key}, before, sources)
	http.Redirect(w, r, "/painel/configuracao?aba=parametros&restaurado="+url.QueryEscape(c.Key)+ended, http.StatusSeeOther)
}

// Grupos de acesso (Aula 19.4): tirar um grupo (ou papel) da lista encerra as sessões
// daquela origem, menos a de quem mudou. Sem isso, quem saiu do grupo continuaria com o perfil
// até a sessão vencer (até 8 h). Acrescentar não encerra nada.

// listsBefore: os valores de agora das listas com sessões (de um cartão; "" = todos).
func (a *api) listsBefore(group string) (map[string]string, map[string]string) {
	before, sources := map[string]string{}, map[string]string{}
	for _, g := range a.params.Groups() {
		if group != "" && g.Name != group {
			continue
		}
		for _, f := range g.Fields {
			if f.Sessions != "" {
				before[f.Key], sources[f.Key] = f.Value, f.Sessions
			}
		}
	}
	return before, sources
}

// listItems: as entradas de uma lista da tela (um DN vale inteiro).
func listItems(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.Contains(v, "=") {
		return []string{v}
	}
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// narrowed encerra as sessões das origens cujas listas perderam algum item; devolve o
// pedaço da URL com quantas ("" nenhuma).
func (a *api) narrowed(r *http.Request, s panel.Session, keys []string, before, sources map[string]string) string {
	srcs := map[string]bool{}
	for _, k := range keys {
		src, ok := sources[k]
		if !ok {
			continue
		}
		after := listItems(a.params.Value(k))
		for _, x := range listItems(before[k]) {
			if !slices.ContainsFunc(after, func(y string) bool { return strings.EqualFold(x, y) }) {
				srcs[src] = true
			}
		}
	}
	if len(srcs) == 0 {
		return ""
	}
	list, err := a.store.PanelSessions(r.Context())
	if err != nil {
		a.logger.Error("grupos de acesso: não deu para listar as sessões; encerre em Acesso", "error", err)
		return ""
	}
	var own []byte
	if c, err := r.Cookie(sessionCookie); err == nil {
		own = identity.Hash(c.Value)
	}
	n := 0
	for _, x := range list {
		if !srcs[x.Source] || bytes.Equal(x.Hash, own) {
			continue
		}
		if err := a.store.DeletePanelSession(r.Context(), x.Hash); err != nil {
			a.logger.Error("grupos de acesso: sessão não encerrada", "session_user", x.Username, "error", err)
			continue
		}
		n++
	}
	if n == 0 {
		return ""
	}
	var labels []string
	for src := range srcs {
		labels = append(labels, panel.SourceLabel(src))
	}
	slices.Sort(labels)
	a.audit(r, s, "parametro.sessoes", strings.Join(labels, ", "), fmt.Sprintf("%d sessão(ões) encerrada(s): grupo tirado da lista", n))
	a.logger.Info("grupos de acesso: sessões encerradas", "quantas", n, "origens", strings.Join(labels, ","), "username", s.Username,
		"request_id", RequestID(r.Context()))
	return "&encerradas=" + strconv.Itoa(n)
}

// paramsChanged: o e-mail para a equipe, sem segurar a resposta (o SMTP pode demorar).
func (a *api) paramsChanged(s panel.Session, lines []string) {
	a.chromeStale()
	if !a.notifyOn() {
		return
	}
	n := a.notifier.n
	body := fmt.Sprintf("Parâmetros do patchd alterados por %s (%s) em %s:\n\n- %s",
		s.Username, panel.SourceLabel(s.Source), a.now().UTC().Format("02/01/2006 15:04")+" UTC", strings.Join(lines, "\n- "))
	if a.publicURL != "" {
		body += "\n\nPainel: " + strings.TrimRight(a.publicURL, "/") + "/painel/configuracao?aba=parametros"
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := n.Send(ctx, "Parâmetros alterados", body); err != nil {
			a.logger.Warn("aviso de parâmetros alterados não enviado", "error", err)
		}
	}()
}

// renderParams: a aba de novo, com o que foi digitado e os problemas (nada foi gravado).
func (a *api) renderParams(w http.ResponseWriter, r *http.Request, s panel.Session, typed map[string]string, problems map[string]string, msg string) {
	v := settingsView{pageBase: a.baseFor(r.Context(), s, "Configuração", "configuracao"), Tab: "parametros"}
	v.Sub = v.Tab
	v.Error = msg
	v.NotifyOn = a.notifyOn()
	v.Params = a.paramsView(typed, problems)
	a.render(w, r, http.StatusUnprocessableEntity, "configuracao", v)
}
