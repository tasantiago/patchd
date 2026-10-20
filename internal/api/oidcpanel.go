package api

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/texto"
)

// Login pelo RHBK (Aula 9.4): o botão leva o navegador ao provedor com state, nonce e o
// desafio do PKCE; a volta traz o código, que o servidor troca pelo ID token e confere.
// A sessão aberta é a mesma do login por senha, com a origem "oidc".

const (
	oidcCookie     = "patchd_oidc"    // o state, para amarrar a volta a este navegador
	oidcLoginTTL   = 10 * time.Minute // tempo para a pessoa concluir o login no provedor
	oidcMaxPending = 1000             // logins em andamento guardados (protege a memória)
	oidcMaxPerIP   = 10               // de um mesmo IP (Aula 20.4b): uma pessoa não passa disso
	oidcTimeout    = 15 * time.Second // descoberta, JWKS e troca do código
	oidcPath       = "/painel/oidc/"  // o cookie do state só vai para essas rotas
)

// WithOIDC liga o login pelo RHBK.
func WithOIDC(p *oidc.Provider) Option {
	return func(a *api) { a.oidc = p }
}

// oidcPending guarda os logins em andamento: state → o resto. Cada um vale uma vez.
//
// O início do login não tem autenticação (é o botão da tela de login). Aula 20.4b (achado F7
// da varredura ativa): com um teto que recusava, mil pedidos de qualquer um bloqueavam todo
// login pelo RHBK por 10 minutos. Agora nunca recusa: cada IP guarda no máximo
// oidcMaxPerIP (sai o mais velho dele) e, com a tabela cheia, sai o mais velho de todos.
// Quem enche só derruba os próprios logins, ou precisa de centenas de endereços e de
// repetir dentro do minuto em que alguém está no provedor; e quem perdeu o login clica de
// novo.
type oidcPending struct {
	mu     sync.Mutex
	m      map[string]pendingLogin
	warned time.Time // último aviso de tabela cheia no log
}

type pendingLogin struct {
	login   oidc.Login
	ip      string
	created time.Time
	expires time.Time
}

// put guarda o login. full: a tabela estava cheia e o mais velho de todos saiu; warn: é hora
// de avisar no log (no máximo uma vez por minuto).
func (p *oidcPending) put(l oidc.Login, ip string, now time.Time) (full, warn bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]pendingLogin{}
	}
	var oldest, oldestIP string
	perIP := 0
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
			continue
		}
		if oldest == "" || v.created.Before(p.m[oldest].created) {
			oldest = k
		}
		if v.ip == ip {
			perIP++
			if oldestIP == "" || v.created.Before(p.m[oldestIP].created) {
				oldestIP = k
			}
		}
	}
	switch {
	case perIP >= oidcMaxPerIP:
		delete(p.m, oldestIP)
	case len(p.m) >= oidcMaxPending:
		delete(p.m, oldest)
		full = true
		if now.Sub(p.warned) >= time.Minute {
			p.warned, warn = now, true
		}
	}
	p.m[l.State] = pendingLogin{login: l, ip: ip, created: now, expires: now.Add(oidcLoginTTL)}
	return full, warn
}

// take tira o login do state (uso único) se ainda vale.
func (p *oidcPending) take(state string, now time.Time) (oidc.Login, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[state]
	delete(p.m, state)
	if !ok || now.After(v.expires) {
		return oidc.Login{}, false
	}
	return v.login, true
}

// oidcStart manda o navegador ao provedor.
func (a *api) oidcStart(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
	defer cancel()
	if err := a.oidc.Discover(ctx); err != nil {
		a.logger.Error("login pelo RHBK: provedor indisponível", "error", err, "request_id", RequestID(r.Context()))
		a.renderLogin(w, http.StatusBadGateway, loginData{Error: "O RHBK não respondeu. Tente de novo ou entre com usuário e senha.", AD: a.directory != nil})
		return
	}
	l := oidc.NewLogin()
	if _, warn := a.oidcLogins.put(l, remoteIP(r), a.now()); warn {
		a.logger.Warn("logins pelo RHBK em andamento no limite: o mais velho saiu (inícios de login de muitos endereços)",
			"limite", oidcMaxPending, "remote", remoteIP(r), "request_id", RequestID(r.Context()))
	}
	// SameSite=Lax: a volta do provedor é uma navegação de outro site, e o cookie Strict
	// não viria. HttpOnly e só para /painel/oidc/.
	// #nosec G124 -- Lax é obrigatório na volta do provedor; Secure segue o TLS da conexão
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: l.State, Path: oidcPath, MaxAge: int(oidcLoginTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.oidc.AuthURL(l), http.StatusSeeOther)
}

// oidcReturn recebe a volta do provedor, troca o código e abre a sessão.
func (a *api) oidcReturn(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	// #nosec G124 -- apaga o cookie do state; os atributos repetem os de quem o criou
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: oidcPath, MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	q := r.URL.Query()
	fail := func(status int, msg, reason string, err error) {
		args := []any{"reason", reason, "remote", remoteIP(r), "request_id", RequestID(r.Context())}
		if err != nil {
			args = append(args, "error", err.Error())
		}
		a.count(loginsOf, "rhbk", "recusado")
		a.logger.Warn("login pelo RHBK recusado", args...)
		a.auditAs(r, "", panel.SourceOIDC, "login.recusado", "", reason)
		a.renderLogin(w, status, loginData{Error: msg, AD: a.directory != nil})
	}
	if e := q.Get("error"); e != "" {
		desc := texto.Clip(q.Get("error_description"), 200) // vai para a auditoria
		fail(http.StatusUnauthorized, "O RHBK não concluiu o login.", "o provedor respondeu "+e+": "+desc, nil)
		return
	}
	// O state tem de ser o deste navegador (cookie) e de um login em andamento (memória).
	c, err := r.Cookie(oidcCookie)
	state := q.Get("state")
	if err != nil || state == "" || c.Value != state {
		fail(http.StatusBadRequest, "O login pelo RHBK expirou ou foi aberto em outra aba. Tente de novo.", "state ausente ou de outro navegador", nil)
		return
	}
	l, ok := a.oidcLogins.take(state, a.now())
	if !ok || q.Get("code") == "" {
		fail(http.StatusBadRequest, "O login pelo RHBK expirou ou foi aberto em outra aba. Tente de novo.", "state desconhecido, vencido ou já usado", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
	defer cancel()
	id, err := a.oidc.Exchange(ctx, q.Get("code"), l)
	switch {
	case errors.Is(err, panel.ErrNoRole):
		a.count(loginsOf, "rhbk", "sem_grupo")
		a.logger.Warn("login pelo RHBK recusado", "reason", "sem grupo do painel", "username", id.Username,
			"grupos", id.Roles, "remote", remoteIP(r), "request_id", RequestID(r.Context()))
		a.auditAs(r, id.Username, panel.SourceOIDC, "login.recusado", "", "sem grupo do painel")
		a.renderLogin(w, http.StatusForbidden, loginData{Error: "Seu usuário não está em nenhum grupo do painel.", AD: a.directory != nil})
		return
	case err != nil:
		fail(http.StatusUnauthorized, "Não foi possível concluir o login pelo RHBK.", "troca do código ou ID token", err)
		return
	}
	if !a.openSession(w, r, id.Username, id.Name, id.Role, panel.SourceOIDC) {
		return
	}
	a.count(loginsOf, "rhbk", "ok")
	// Uma página, e não um redirecionamento: o cookie da sessão é SameSite=Strict, e uma
	// cadeia de redirecionamentos que começou no provedor (outro site) não o levaria ao
	// /painel/. A navegação que esta página faz já parte do próprio painel.
	pageHeaders(w)
	_ = oidcDoneTemplate.Execute(w, nil)
}

var oidcDoneTemplate = template.Must(template.New("oidc").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=/painel/">
<title>patchd · entrando</title>` + faviconLink + styleLink + `</head><body class="entrar">
<main class="entrar volta"><p class="card">Login concluído. <a href="/painel/">Abrir o painel</a>.</p></main></body></html>
`))
