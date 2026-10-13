package api

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
)

// sessionCookie é o cookie da sessão do painel. Leva o segredo; o banco guarda o hash.
const sessionCookie = "patchd_sessao"

// Limites do login (Aula 7.2). As falhas contam por usuário+IP (quem erra a própria
// senha) e por IP (quem testa muitos nomes). A janela é a mesma: 15 minutos.
const (
	loginWindow       = 15 * time.Minute
	loginFailsPerUser = 5
	loginFailsPerIP   = 20
	// Conferências de senha simultâneas: cada uma gasta ~0,2 s de CPU (PBKDF2); sem o
	// limite, uma rajada de logins pararia o servidor.
	loginConcurrent = 4
	maxLoginBody    = 8 << 10
	touchEvery      = time.Minute // o uso da sessão é gravado no máximo uma vez por minuto
)

// PanelStore é o que o painel precisa do armazenamento (parte de Store).
type PanelStore interface {
	PanelUser(ctx context.Context, name string) (panel.User, bool, error)
	CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error
	PanelSession(ctx context.Context, tokenHash []byte) (panel.Session, bool, error)
	TouchPanelSession(ctx context.Context, tokenHash []byte, at time.Time) error
	DeletePanelSession(ctx context.Context, tokenHash []byte) error
	DeleteExpiredPanelSessions(ctx context.Context, now time.Time) (int64, error)
}

// loginLimiter conta as falhas de login na memória do processo. Reiniciar o servidor
// zera a contagem: aceitável, porque reiniciar não está ao alcance de quem tenta senhas.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < loginWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

// blocked diz se o usuário (neste IP) ou o IP já esgotaram as tentativas.
func (l *loginLimiter) blocked(user, ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent("u\x00"+user+"\x00"+ip, now)) >= loginFailsPerUser ||
		len(l.recent("ip\x00"+ip, now)) >= loginFailsPerIP
}

func (l *loginLimiter) fail(user, ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	// Limpeza das chaves antigas: sem ela, nomes inventados acumulariam para sempre.
	if len(l.fails) > 10_000 {
		for k := range l.fails {
			l.recent(k, now)
		}
	}
	for _, k := range []string{"u\x00" + user + "\x00" + ip, "ip\x00" + ip} {
		l.fails[k] = append(l.fails[k], now)
	}
}

// succeed zera as falhas do usuário neste IP (as do IP continuam contando).
func (l *loginLimiter) succeed(user, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, "u\x00"+user+"\x00"+ip)
}

// remoteIP é o IP de quem conectou. Atrás de um proxy reverso (Módulo 9), viria do
// cabeçalho que o proxy preenche, e só dele.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type sessionKey struct{}

// SessionFrom devolve a sessão do painel que autenticou a requisição.
func SessionFrom(ctx context.Context) (panel.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(panel.Session)
	return s, ok
}

// currentSession lê o cookie e devolve a sessão válida. Sessão vencida é apagada.
func (a *api) currentSession(r *http.Request) (panel.Session, []byte, bool, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !strings.HasPrefix(c.Value, panel.SessionPrefix) {
		return panel.Session{}, nil, false, nil
	}
	hash := identity.Hash(c.Value)
	s, found, err := a.store.PanelSession(r.Context(), hash)
	if err != nil || !found {
		return panel.Session{}, nil, false, err
	}
	now := a.now()
	if !s.Valid(now) {
		if err := a.store.DeletePanelSession(r.Context(), hash); err != nil {
			return panel.Session{}, nil, false, err
		}
		return panel.Session{}, nil, false, nil
	}
	if now.Sub(s.LastSeenAt) >= touchEvery {
		if err := a.store.TouchPanelSession(r.Context(), hash, now); err != nil {
			return panel.Session{}, nil, false, err
		}
		s.LastSeenAt = now
	}
	return s, hash, true, nil
}

// sessionAuth protege as rotas de leitura da API: sem sessão, 401 em JSON.
func (a *api) sessionAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _, ok, err := a.currentSession(r)
		if err != nil {
			a.internalError(w, r, "conferir sessão", err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sessão do painel ausente ou vencida: entre em /painel/entrar")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)))
	})
}

// panelPage protege as páginas do painel: sem sessão, volta para o login.
func (a *api) panelPage(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _, ok, err := a.currentSession(r)
		if err != nil {
			a.internalError(w, r, "conferir sessão", err)
			return
		}
		if !ok {
			http.Redirect(w, r, "/painel/entrar", http.StatusSeeOther)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)))
	})
}

// sameOrigin recusa POSTs que um navegador enviou a partir de outro site (CSRF), com a
// proteção da biblioteca padrão (Go 1.25): Sec-Fetch-Site, ou Origin contra o Host.
// Pedidos sem esses cabeçalhos (curl, scripts) passam: não vêm de navegador, não há CSRF.
func (a *api) sameOrigin(next http.HandlerFunc) http.Handler {
	cop := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cop.Check(r); err != nil {
			a.logger.Warn("formulário do painel recusado: outra origem", "error", err,
				"remote", remoteIP(r), "request_id", RequestID(r.Context()))
			http.Error(w, "pedido de outra origem recusado", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// pageHeaders: páginas do painel nunca vão para cache nem para dentro de frames, e só
// carregam o que vem delas mesmas.
func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

type loginData struct {
	Username string
	Error    string
	AD       bool // o login é pelo AD: a dica diz para usar o usuário da rede
	OIDC     bool // botão "Entrar com o RHBK" (Aula 9.4)
	Brand    string
}

func (a *api) renderLogin(w http.ResponseWriter, status int, d loginData) {
	d.OIDC, d.Brand = a.oidc != nil, a.brand
	pageHeaders(w)
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, d)
}

// loginForm mostra o formulário; com sessão válida, vai direto ao painel.
func (a *api) loginForm(w http.ResponseWriter, r *http.Request) {
	if _, _, ok, err := a.currentSession(r); err == nil && ok {
		http.Redirect(w, r, "/painel/", http.StatusSeeOther)
		return
	}
	a.renderLogin(w, http.StatusOK, loginData{AD: a.directory != nil})
}

// login confere usuário e senha e abre a sessão. A mensagem de erro é sempre a mesma:
// dizer "usuário não existe" ajudaria quem tenta adivinhar.
func (a *api) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		a.renderLogin(w, http.StatusBadRequest, loginData{Error: "Formulário inválido.", AD: a.directory != nil})
		return
	}
	user := panel.NormalizeUsername(r.PostFormValue("usuario"))
	pw := r.PostFormValue("senha")
	ip, now := remoteIP(r), a.now()
	logUser := user
	if !panel.ValidUsername(user) {
		logUser = "(fora do padrão)" // o que se digita no campo errado pode ser a senha
	}

	if a.limiter.blocked(user, ip, now) {
		a.count(loginsOf, "senha", "bloqueado")
		a.logger.Warn("login bloqueado: tentativas demais", "username", logUser, "remote", ip, "request_id", RequestID(r.Context()))
		a.renderLogin(w, http.StatusTooManyRequests, loginData{Username: user, Error: "Tentativas demais. Aguarde 15 minutos.", AD: a.directory != nil})
		return
	}

	res, err := a.authenticate(r, user, pw)
	if err != nil {
		a.internalError(w, r, "conferir usuário do painel", err)
		return
	}
	if !res.ok {
		a.count(loginsOf, "senha", "recusado")
		a.limiter.fail(user, ip, now)
		a.logger.Warn("login recusado", "username", logUser, "reason", res.reason, "remote", ip, "request_id", RequestID(r.Context()))
		a.renderLogin(w, http.StatusUnauthorized, loginData{Username: user, Error: "Usuário ou senha inválidos.", AD: a.directory != nil})
		return
	}
	a.limiter.succeed(user, ip)
	if !a.openSession(w, r, user, res.role, res.source) {
		return
	}
	a.count(loginsOf, "senha", "ok")
	http.Redirect(w, r, "/painel/", http.StatusSeeOther)
}

// openSession abre a sessão e põe o cookie (login por senha e pelo RHBK). Em erro, já
// respondeu e devolve false.
func (a *api) openSession(w http.ResponseWriter, r *http.Request, user, role, source string) bool {
	now := a.now()
	if n, err := a.store.DeleteExpiredPanelSessions(r.Context(), now); err != nil {
		a.logger.Warn("limpeza das sessões vencidas", "error", err)
	} else if n > 0 {
		a.logger.Info("sessões vencidas apagadas", "count", n)
	}
	// Segredo novo a cada login: um cookie antigo plantado no navegador nunca vira sessão.
	plain, hash := identity.NewSecret(panel.SessionPrefix)
	s := panel.Session{Username: user, Role: role, Source: source,
		CreatedAt: now, ExpiresAt: now.Add(panel.SessionMaxAge), LastSeenAt: now}
	if err := a.store.CreatePanelSession(r.Context(), hash, s); err != nil {
		a.internalError(w, r, "abrir sessão do painel", err)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: plain, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
	a.logger.Info("login no painel", "username", s.Username, "role", s.Role, "source", s.Source,
		"remote", remoteIP(r), "request_id", RequestID(r.Context()))
	return true
}

// Directory confere a senha num diretório externo (o AD, Aula 7.3) e devolve o perfil.
// Erros: panel.ErrBadCredentials e panel.ErrNoRole recusam o login; qualquer outro quer
// dizer "não foi possível conferir", e só aí a conta local de contingência é tentada.
type Directory interface {
	Authenticate(ctx context.Context, user, password string) (role string, err error)
}

// WithDirectory liga o login pelo AD.
func WithDirectory(d Directory) Option {
	return func(a *api) { a.directory = d }
}

type authResult struct {
	ok           bool
	role, source string
	reason       string // por que foi recusado (só para o log)
}

// authenticate decide quem confere a senha. Com o AD ligado, só ele; a conta local
// entra apenas quando o AD não pôde responder (rede, certificado, configuração). Se a
// conta local valesse sempre, seria uma porta lateral fora do ciclo de vida do AD
// (desligamento, bloqueio, troca de senha).
func (a *api) authenticate(r *http.Request, user, pw string) (authResult, error) {
	if a.directory != nil {
		role, err := a.directory.Authenticate(r.Context(), user, pw)
		switch {
		case err == nil:
			return authResult{ok: true, role: role, source: panel.SourceAD}, nil
		case errors.Is(err, panel.ErrBadCredentials), errors.Is(err, panel.ErrNoRole):
			return authResult{reason: "AD: " + err.Error()}, nil
		}
		a.logger.Warn("AD indisponível: o login tenta a conta local de contingência",
			"error", err.Error(), "request_id", RequestID(r.Context()))
	}
	u, ok, err := a.checkLocal(r.Context(), user, pw)
	if err != nil || !ok {
		reason := "conta local recusada"
		if a.directory != nil {
			reason = "AD indisponível e conta local recusada"
		}
		return authResult{reason: reason}, err
	}
	return authResult{ok: true, role: u.Role, source: panel.SourceLocal}, nil
}

// checkLocal confere a senha do usuário local. Senha vazia nunca confere (no AD, um bind
// com senha vazia "funciona" sem autenticar: a regra vale desde já para as duas origens).
func (a *api) checkLocal(ctx context.Context, user, pw string) (panel.User, bool, error) {
	if pw == "" || len(pw) > panel.MaxPasswordBytes || !panel.ValidUsername(user) {
		return panel.User{}, false, nil
	}
	u, found, err := a.store.PanelUser(ctx, user)
	if err != nil {
		return panel.User{}, false, err
	}
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	case <-ctx.Done():
		return panel.User{}, false, ctx.Err()
	}
	if !found {
		return panel.User{}, panel.CheckPasswordUnknownUser(pw), nil
	}
	return u, panel.CheckPassword(u.PasswordHash, pw), nil
}

// logout encerra a sessão no servidor e apaga o cookie.
func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	s, hash, ok, err := a.currentSession(r)
	if err != nil {
		a.internalError(w, r, "conferir sessão", err)
		return
	}
	if ok {
		if err := a.store.DeletePanelSession(r.Context(), hash); err != nil {
			a.internalError(w, r, "encerrar sessão do painel", err)
			return
		}
		a.logger.Info("saída do painel", "username", s.Username, "remote", remoteIP(r), "request_id", RequestID(r.Context()))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	http.Redirect(w, r, "/painel/entrar", http.StatusSeeOther)
}

// pageStyle: a tela de login e a de volta do RHBK, no mesmo padrão do painel (Aula 11.3).
const pageStyle = `<style>` + themeCSS + `
main.entrar{max-width:26rem;padding-top:3rem}main.entrar h1{margin-top:0}
main.entrar label{display:block;margin-top:.9rem;font-weight:500}main.entrar input{display:block;width:100%;margin-top:.3rem}
main.entrar button{margin-top:1.3rem;width:100%;padding:.55rem}main.entrar small{color:var(--muted)}
a.sso{display:block;text-align:center;padding:.55rem;border:1px solid var(--azul);border-radius:6px;text-decoration:none;color:var(--azul);font-weight:500}
a.sso:hover{background:#eef5fd}.ou{text-align:center;color:var(--muted);margin:1rem 0 0}
</style>`

var loginTemplate = template.Must(template.New("entrar").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Brand}} · entrar</title>` + faviconLink + pageStyle + `</head><body>
<header class="topo"><span class="marca">` + shieldSVG + `<span>{{.Brand}}</span></span></header>
<main class="entrar"><div class="card">
<h1>Entrar</h1>
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{end}}
{{if .OIDC}}<a class="sso" href="/painel/oidc/entrar">Entrar com o RHBK (login único)</a>
<p class="ou">ou com usuário e senha</p>{{end}}
<form method="post" action="/painel/entrar">
<label>Usuário <input name="usuario" value="{{.Username}}" autocomplete="username" required autofocus></label>
{{if .AD}}<small>O usuário da rede, sem o domínio.</small>{{end}}
<label>Senha <input name="senha" type="password" autocomplete="current-password" required></label>
<button type="submit">Entrar</button>
</form></div></main></body></html>
`))
