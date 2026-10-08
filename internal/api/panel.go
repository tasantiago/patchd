package api

import (
	"context"
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
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

type loginData struct {
	Username string
	Error    string
}

func (a *api) renderLogin(w http.ResponseWriter, status int, d loginData) {
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
	a.renderLogin(w, http.StatusOK, loginData{})
}

// login confere usuário e senha e abre a sessão. A mensagem de erro é sempre a mesma:
// dizer "usuário não existe" ajudaria quem tenta adivinhar.
func (a *api) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		a.renderLogin(w, http.StatusBadRequest, loginData{Error: "Formulário inválido."})
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
		a.logger.Warn("login bloqueado: tentativas demais", "username", logUser, "remote", ip, "request_id", RequestID(r.Context()))
		a.renderLogin(w, http.StatusTooManyRequests, loginData{Username: user, Error: "Tentativas demais. Aguarde 15 minutos."})
		return
	}

	u, ok, err := a.checkLocal(r.Context(), user, pw)
	if err != nil {
		a.internalError(w, r, "conferir usuário do painel", err)
		return
	}
	if !ok {
		a.limiter.fail(user, ip, now)
		a.logger.Warn("login recusado", "username", logUser, "remote", ip, "request_id", RequestID(r.Context()))
		a.renderLogin(w, http.StatusUnauthorized, loginData{Username: user, Error: "Usuário ou senha inválidos."})
		return
	}
	a.limiter.succeed(user, ip)

	if n, err := a.store.DeleteExpiredPanelSessions(r.Context(), now); err != nil {
		a.logger.Warn("limpeza das sessões vencidas", "error", err)
	} else if n > 0 {
		a.logger.Info("sessões vencidas apagadas", "count", n)
	}
	// Segredo novo a cada login: um cookie antigo plantado no navegador nunca vira sessão.
	plain, hash := identity.NewSecret(panel.SessionPrefix)
	s := panel.Session{Username: u.Name, Role: u.Role, Source: panel.SourceLocal,
		CreatedAt: now, ExpiresAt: now.Add(panel.SessionMaxAge), LastSeenAt: now}
	if err := a.store.CreatePanelSession(r.Context(), hash, s); err != nil {
		a.internalError(w, r, "abrir sessão do painel", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: plain, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
	a.logger.Info("login no painel", "username", u.Name, "role", u.Role, "source", s.Source,
		"remote", ip, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, "/painel/", http.StatusSeeOther)
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

type homeData struct {
	Session  panel.Session
	Machines int
}

// home é a página inicial do painel (as telas de verdade chegam nas próximas aulas).
func (a *api) home(w http.ResponseWriter, r *http.Request) {
	s, _ := SessionFrom(r.Context())
	list, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	pageHeaders(w)
	_ = homeTemplate.Execute(w, homeData{Session: s, Machines: len(list)})
}

const pageStyle = `<style>
body{font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem;color:#1b1b1b}
label{display:block;margin-top:1rem}input{width:100%;padding:.4rem;box-sizing:border-box}
button{margin-top:1.2rem;padding:.4rem 1rem}.erro{color:#a40000}dt{font-weight:600;margin-top:.6rem}
</style>`

var loginTemplate = template.Must(template.New("entrar").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>patchd · entrar</title>` + pageStyle + `</head><body>
<h1>patchd</h1>
{{if .Error}}<p class="erro" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/painel/entrar">
<label>Usuário <input name="usuario" value="{{.Username}}" autocomplete="username" required autofocus></label>
<label>Senha <input name="senha" type="password" autocomplete="current-password" required></label>
<button type="submit">Entrar</button>
</form></body></html>
`))

var homeTemplate = template.Must(template.New("painel").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>patchd · painel</title>` + pageStyle + `</head><body>
<h1>patchd</h1>
<dl>
<dt>Usuário</dt><dd>{{.Session.Username}} ({{.Session.Source}})</dd>
<dt>Perfil</dt><dd>{{.Session.Role}}</dd>
<dt>Sessão vence</dt><dd>{{.Session.ExpiresAt.Format "02/01/2006 15:04"}} UTC, ou após 30 min sem uso</dd>
<dt>Máquinas registradas</dt><dd>{{.Machines}}</dd>
</dl>
<p><a href="/api/v1/machines">/api/v1/machines</a> · <a href="/api/v1/identity-links">/api/v1/identity-links</a></p>
<form method="post" action="/painel/sair"><button type="submit">Sair</button></form>
</body></html>
`))
