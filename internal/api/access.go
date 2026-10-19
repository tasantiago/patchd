package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
)

// Configuração › Acesso (Aula 15.3, opção A): como o painel autentica, sem editar nada.
// O AD e o RHBK continuam no .env e nos arquivos de segredo; a tela mostra o que está
// ligado e testa (um login de teste no AD, a descoberta do RHBK). Os usuários locais de
// contingência são administrados aqui: criar e trocar a senha geram uma senha forte,
// mostrada uma única vez; remover encerra as sessões. Ao menos um admin local fica sempre.

// ADVar: uma variável do AD e se está definida. O valor não aparece (servidor, domínio e
// grupos ficam só no .env, como no log desde a Aula 7.3).
type ADVar struct {
	Name, Note string
	Set        bool
}

// ADCheckResult: o login de teste no AD, como o patchd-server ad check mostra.
type ADCheckResult struct {
	Unavailable                    string // o AD não respondeu: o motivo
	TLSVersion                     string
	Name                           string // displayName da conta (Aula 15.5); vazio se não há
	BadPassword                    bool
	AdminSet, ReadSet, Admin, Read bool
	Role                           string // vazio: o login seria recusado
	// Aula 19.4: por qual grupo e os grupos da tela que o AD não achou.
	AdminVia, ReadVia string
	Missing           []string
}

// ADInfo: as variáveis e o teste; o main liga quando o AD está configurado.
type ADInfo struct {
	Vars  []ADVar
	Check func(ctx context.Context, user, password string) (ADCheckResult, error)
}

// WithADInfo liga o cartão do AD na aba Acesso.
func WithADInfo(i ADInfo) Option {
	return func(a *api) { a.adInfo = &i }
}

type localUserRow struct {
	Name, Role, Created string
	Self, Last          bool // a conta da sessão atual; o único admin local
}

type oidcView struct {
	Issuer, ClientID, Claim, AdminRole, ReadRole string
}

// sessionRow: uma sessão aberta (Aula 15.4). ID é o hash do segredo em hexadecimal:
// identifica a sessão sem permitir usá-la.
type sessionRow struct {
	ID, User, Name, Source, Role, Created, Seen, Expires string
	Current                                              bool
}

// sessUser: uma opção do "Encerrar todas as sessões de", com o nome quando há.
type sessUser struct{ User, Name string }

type accessView struct {
	Sessions   []sessionRow
	SessUsers  []sessUser // quem tem sessão aberta, menos você
	AD         *ADInfo
	ADResult   *adResultView
	OIDC       *oidcView
	OIDCResult string
	OIDCOK     bool
	Users      []localUserRow
	Local      string // o papel dos usuários locais: contingência ou o login principal
}

type adResultView struct {
	User  string
	Lines []settingRow
	OK    bool
}

// accessView monta a aba Acesso.
func (a *api) accessView(r *http.Request, s panel.Session, v *settingsView) error {
	acc := &accessView{AD: a.adInfo}
	if a.oidc != nil {
		c := a.oidc.Public()
		acc.OIDC = &oidcView{Issuer: c.Issuer, ClientID: c.ClientID, Claim: c.RolesClaim, AdminRole: c.AdminRole, ReadRole: dash(c.ReadRole)}
	}
	switch {
	case a.directory != nil:
		acc.Local = "contingência: entram só quando o AD não responde"
	case a.oidc != nil:
		acc.Local = "o formulário de senha; o RHBK entra pelo botão"
	default:
		acc.Local = "o login do painel (sem AD nem RHBK)"
	}
	users, err := a.store.PanelUsers(r.Context())
	if err != nil {
		return err
	}
	admins := 0
	for _, u := range users {
		if u.Role == panel.RoleAdmin {
			admins++
		}
	}
	for _, u := range users {
		acc.Users = append(acc.Users, localUserRow{Name: u.Name, Role: u.Role, Created: a.stamp(u.CreatedAt),
			Self: s.Source == panel.SourceLocal && s.Username == u.Name, Last: u.Role == panel.RoleAdmin && admins == 1})
	}
	if err := a.sessionRows(r, s, acc); err != nil {
		return err
	}
	v.Access = acc
	return nil
}

// sessionRows: as sessões ainda válidas, a desta requisição marcada.
func (a *api) sessionRows(r *http.Request, s panel.Session, acc *accessView) error {
	list, err := a.store.PanelSessions(r.Context())
	if err != nil {
		return err
	}
	var mine []byte
	if c, err := r.Cookie(sessionCookie); err == nil {
		mine = identity.Hash(c.Value)
	}
	now := a.now()
	users := map[string]string{} // usuário → nome de exibição
	for _, x := range list {
		if !x.Valid(now) {
			continue
		}
		exp := x.ExpiresAt
		if idle := x.LastSeenAt.Add(panel.SessionIdle); idle.Before(exp) {
			exp = idle
		}
		row := sessionRow{ID: hex.EncodeToString(x.Hash), User: x.Username, Name: x.DisplayName, Source: panel.SourceLabel(x.Source), Role: x.Role,
			Created: a.stamp(x.CreatedAt), Seen: ago(now.Sub(x.LastSeenAt)), Expires: a.stamp(exp), Current: bytes.Equal(x.Hash, mine)}
		acc.Sessions = append(acc.Sessions, row)
		if x.Username != s.Username {
			if users[x.Username] == "" {
				users[x.Username] = x.DisplayName
			}
		}
	}
	for _, u := range slices.Sorted(maps.Keys(users)) {
		acc.SessUsers = append(acc.SessUsers, sessUser{User: u, Name: users[u]})
	}
	return nil
}

// endSessionAction: POST /painel/configuracao/acesso/sessoes/{id}/encerrar. A da própria
// requisição não: para ela existe o Sair.
func (a *api) endSessionAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	hash, err := hex.DecodeString(r.PathValue("id"))
	if err != nil || len(hash) != sha256.Size {
		http.NotFound(w, r)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && bytes.Equal(identity.Hash(c.Value), hash) {
		http.Redirect(w, r, "/painel/configuracao?aba=acesso&erro=propria-sessao", http.StatusSeeOther)
		return
	}
	target, found, err := a.store.PanelSession(r.Context(), hash)
	if err != nil {
		a.internalError(w, r, "ler sessão", err)
		return
	}
	if !found {
		http.Redirect(w, r, "/painel/configuracao?aba=acesso&erro=sessao", http.StatusSeeOther)
		return
	}
	if err := a.store.DeletePanelSession(r.Context(), hash); err != nil {
		a.internalError(w, r, "encerrar sessão", err)
		return
	}
	a.logger.Info("sessão encerrada pelo painel", "session_user", target.Username, "source", target.Source, "username", s.Username,
		"request_id", RequestID(r.Context()))
	a.audit(r, s, "sessao.encerrar", target.Username, "origem "+panel.SourceLabel(target.Source))
	http.Redirect(w, r, "/painel/configuracao?aba=acesso&"+url.Values{"encerrada": {target.Username}}.Encode(), http.StatusSeeOther)
}

// endUserSessionsAction: POST /painel/configuracao/acesso/sessoes/usuario. Todas as
// sessões de alguém, de qualquer origem: tirar do grupo do AD não derruba quem já entrou.
func (a *api) endUserSessionsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if !parseSmallForm(w, r) {
		return
	}
	user := panel.NormalizeUsername(r.PostFormValue("usuario"))
	switch {
	case !panel.ValidUsername(user):
		http.Redirect(w, r, "/painel/configuracao?aba=acesso&erro=sessao", http.StatusSeeOther)
		return
	case user == s.Username:
		http.Redirect(w, r, "/painel/configuracao?aba=acesso&erro=propria-sessao", http.StatusSeeOther)
		return
	}
	n, err := a.store.DeleteUserSessions(r.Context(), user)
	if err != nil {
		a.internalError(w, r, "encerrar sessões", err)
		return
	}
	a.logger.Info("sessões encerradas pelo painel", "session_user", user, "count", n, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "sessao.encerrar_todas", user, fmt.Sprintf("%d sessão(ões)", n))
	http.Redirect(w, r, "/painel/configuracao?aba=acesso&"+url.Values{"encerradas": {strconv.FormatInt(n, 10)}, "de": {user}}.Encode(), http.StatusSeeOther)
}

// renderAccess: a aba Acesso, com um aviso ou o resultado de um teste.
func (a *api) renderAccess(w http.ResponseWriter, r *http.Request, s panel.Session, status int, edit func(*settingsView)) {
	v := settingsView{pageBase: a.baseFor(r.Context(), s, "Configuração", "configuracao"), Tab: "acesso"}
	if err := a.accessView(r, s, &v); err != nil {
		a.internalError(w, r, "montar a aba Acesso", err)
		return
	}
	if edit != nil {
		edit(&v)
	}
	a.render(w, r, status, "configuracao", v)
}

func parseSmallForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return false
	}
	return true
}

// testADAction: POST /painel/configuracao/acesso/testar-ad. Um login de teste: não abre
// sessão. A senha não vai para log nem volta na página; as falhas contam no mesmo limite
// do login, para o formulário não virar um caminho para testar senhas.
func (a *api) testADAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if !parseSmallForm(w, r) {
		return
	}
	if a.adInfo == nil || a.adInfo.Check == nil {
		http.NotFound(w, r)
		return
	}
	user := panel.NormalizeUsername(r.PostFormValue("usuario"))
	pw := r.PostFormValue("senha")
	if !panel.ValidUsername(user) || pw == "" {
		a.renderAccess(w, r, s, http.StatusUnprocessableEntity, func(v *settingsView) {
			v.Error = "Informe o usuário da rede (sem o domínio) e a senha."
		})
		return
	}
	ip, now := remoteIP(r), a.now()
	if a.limiter.blocked(user, ip, now) {
		a.logger.Warn("teste de login no AD bloqueado: tentativas demais", "tested_user", user, "username", s.Username, "request_id", RequestID(r.Context()))
		a.renderAccess(w, r, s, http.StatusTooManyRequests, func(v *settingsView) { v.Error = "Tentativas demais para esse usuário. Aguarde 15 minutos." })
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := a.adInfo.Check(ctx, user, pw)
	if err != nil {
		a.logger.Warn("teste de login no AD com erro", "tested_user", user, "username", s.Username, "error", err, "request_id", RequestID(r.Context()))
		a.renderAccess(w, r, s, http.StatusOK, func(v *settingsView) { v.Error = "O teste falhou: " + err.Error() })
		return
	}
	if res.BadPassword {
		a.limiter.fail(user, ip, now)
	}
	view := adResultLines(user, res)
	a.logger.Info("teste de login no AD pelo painel", "tested_user", user, "ok", view.OK, "role", res.Role, "username", s.Username,
		"request_id", RequestID(r.Context()))
	a.audit(r, s, "ad.testar", user, fmt.Sprintf("ok=%t perfil=%q", view.OK, res.Role))
	a.renderAccess(w, r, s, http.StatusOK, func(v *settingsView) { v.Access.ADResult = &view })
}

// adResultLines: o relatório do teste, como o ad check.
func adResultLines(user string, res ADCheckResult) adResultView {
	v := adResultView{User: user}
	add := func(label, value, level string) { v.Lines = append(v.Lines, settingRow{label, value, level}) }
	if res.Unavailable != "" {
		add("Conexão LDAPS", "falhou: "+res.Unavailable, "erro")
		add("Resultado", "o AD não respondeu; no painel, valeria a conta local de contingência", "erro")
		return v
	}
	add("Conexão LDAPS", "ok ("+res.TLSVersion+")", "")
	if res.BadPassword {
		add("Senha", "recusada pelo AD", "erro")
		return v
	}
	add("Senha", "conferida pelo AD", "")
	if res.Name != "" {
		add("Nome de exibição", res.Name, "")
	} else {
		add("Nome de exibição", "vazio no AD: o painel mostra o usuário", "atencao")
	}
	grupo := func(set, member bool, via string) (string, string) {
		switch {
		case !set:
			return "não configurado", ""
		case member && via != "":
			return "membro, por " + via, ""
		case member:
			return "membro", ""
		}
		return "não membro", "atencao"
	}
	g, l := grupo(res.AdminSet, res.Admin, res.AdminVia)
	add("Grupos admin", g, l)
	g, l = grupo(res.ReadSet, res.Read, res.ReadVia)
	add("Grupos leitura", g, l)
	if len(res.Missing) > 0 {
		add("Grupos da tela que o AD não achou", strings.Join(res.Missing, ", ")+" (ficam de fora; confira o nome em Parâmetros › Acesso)", "atencao")
	}
	if res.Role == "" {
		add("Perfil no painel", "nenhum: o login seria recusado", "erro")
		return v
	}
	add("Perfil no painel", res.Role, "")
	v.OK = true
	return v
}

// testOIDCAction: POST /painel/configuracao/acesso/testar-oidc. Busca a descoberta de novo.
func (a *api) testOIDCAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := a.oidc.Probe(ctx)
	a.logger.Info("teste da descoberta do RHBK pelo painel", "ok", err == nil, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "oidc.testar", "", fmt.Sprintf("ok=%t", err == nil))
	a.renderAccess(w, r, s, http.StatusOK, func(v *settingsView) {
		v.Access.OIDCOK = err == nil
		v.Access.OIDCResult = "Descoberta conferida: o RHBK respondeu e se apresenta com o emissor configurado."
		if err != nil {
			v.Access.OIDCResult = err.Error()
		}
	})
}

type passwordView struct {
	pageBase
	User, Role, Password string
	Created              bool
}

// localPasswordPage: a senha aparece uma única vez, na resposta do POST.
func (a *api) localPasswordPage(w http.ResponseWriter, r *http.Request, s panel.Session, v passwordView) {
	v.pageBase = a.baseFor(r.Context(), s, "Usuário local", "configuracao")
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, "senha-local", v)
}

// createLocalUserAction: POST /painel/configuracao/acesso/usuarios.
func (a *api) createLocalUserAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if !parseSmallForm(w, r) {
		return
	}
	name := panel.NormalizeUsername(r.PostFormValue("nome"))
	role := r.PostFormValue("perfil")
	problem := ""
	switch {
	case !panel.ValidUsername(name):
		problem = "Nome: minúsculas, dígitos, ponto, hífen e sublinhado, de 2 a 64 caracteres."
	case !panel.ValidRole(role):
		problem = "Escolha o perfil: admin ou leitura."
	}
	if problem != "" {
		a.renderAccess(w, r, s, http.StatusUnprocessableEntity, func(v *settingsView) { v.Error = problem })
		return
	}
	pw, hash, err := newLocalPassword()
	if err != nil {
		a.internalError(w, r, "gerar senha", err)
		return
	}
	err = a.store.CreatePanelUser(r.Context(), panel.User{Name: name, Role: role, PasswordHash: hash})
	if errors.Is(err, panel.ErrUserExists) {
		a.renderAccess(w, r, s, http.StatusConflict, func(v *settingsView) {
			v.Error = fmt.Sprintf("O usuário %s já existe; para outra senha, use \"Nova senha\" na lista.", name)
		})
		return
	}
	if err != nil {
		a.internalError(w, r, "criar usuário local", err)
		return
	}
	a.logger.Info("usuário local criado pelo painel", "local_user", name, "role", role, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "usuario.criar", name, "perfil "+role)
	a.localPasswordPage(w, r, s, passwordView{User: name, Role: role, Password: pw, Created: true})
}

func newLocalPassword() (pw, hash string, err error) {
	if pw, err = panel.GeneratePassword(); err != nil {
		return "", "", err
	}
	hash, err = panel.HashPassword(pw)
	return pw, hash, err
}

// localUserTarget lê o {name} e confere as travas: a própria conta local não muda por aqui
// (a sessão cairia no meio), e o último admin local não sai.
func (a *api) localUserTarget(w http.ResponseWriter, r *http.Request, s panel.Session, removing bool) (panel.User, bool) {
	name := panel.NormalizeUsername(r.PathValue("name"))
	users, err := a.store.PanelUsers(r.Context())
	if err != nil {
		a.internalError(w, r, "listar usuários locais", err)
		return panel.User{}, false
	}
	var target *panel.User
	admins := 0
	for i := range users {
		if users[i].Role == panel.RoleAdmin {
			admins++
		}
		if users[i].Name == name {
			target = &users[i]
		}
	}
	back := "/painel/configuracao?aba=acesso&erro="
	switch {
	case target == nil:
		http.Redirect(w, r, back+"usuario", http.StatusSeeOther)
	case s.Source == panel.SourceLocal && s.Username == name:
		http.Redirect(w, r, back+"propria", http.StatusSeeOther)
	case removing && target.Role == panel.RoleAdmin && admins == 1:
		http.Redirect(w, r, back+"ultimo-admin", http.StatusSeeOther)
	default:
		return *target, true
	}
	return panel.User{}, false
}

// resetLocalPasswordAction: POST /painel/configuracao/acesso/usuarios/{name}/senha.
func (a *api) resetLocalPasswordAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	u, ok := a.localUserTarget(w, r, s, false)
	if !ok {
		return
	}
	pw, hash, err := newLocalPassword()
	if err != nil {
		a.internalError(w, r, "gerar senha", err)
		return
	}
	if _, err := a.store.SetPanelPassword(r.Context(), u.Name, hash); err != nil {
		a.internalError(w, r, "trocar senha", err)
		return
	}
	a.logger.Info("senha de usuário local trocada pelo painel", "local_user", u.Name, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "usuario.senha", u.Name, "senha nova gerada pelo painel")
	a.localPasswordPage(w, r, s, passwordView{User: u.Name, Role: u.Role, Password: pw})
}

// deleteLocalUserAction: POST /painel/configuracao/acesso/usuarios/{name}/remover.
func (a *api) deleteLocalUserAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	u, ok := a.localUserTarget(w, r, s, true)
	if !ok {
		return
	}
	if _, err := a.store.DeletePanelUser(r.Context(), u.Name); err != nil {
		a.internalError(w, r, "remover usuário local", err)
		return
	}
	a.logger.Info("usuário local removido pelo painel", "local_user", u.Name, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "usuario.remover", u.Name, "")
	http.Redirect(w, r, "/painel/configuracao?aba=acesso&"+url.Values{"removido": {u.Name}}.Encode(), http.StatusSeeOther)
}

// accessFlash: os avisos que voltam por redirecionamento.
func accessFlash(q url.Values, b *pageBase) {
	switch {
	case q.Has("removido"):
		b.Flash = fmt.Sprintf("Usuário local %s removido; as sessões dele foram encerradas.", q.Get("removido"))
	case q.Get("erro") == "usuario":
		b.Error = "Esse usuário local não existe."
	case q.Get("erro") == "propria":
		b.Error = "Essa é a conta da sua sessão: troque a senha ou remova por outra conta (ou pela linha de comando)."
	case q.Get("erro") == "ultimo-admin":
		b.Error = "É o único admin local: a contingência ficaria sem admin. Crie outro antes de remover."
	case q.Has("encerrada"):
		b.Flash = fmt.Sprintf("Sessão de %s encerrada: no próximo clique, o painel pede login.", q.Get("encerrada"))
	case q.Has("encerradas"):
		b.Flash = fmt.Sprintf("%s sessão(ões) de %s encerrada(s).", q.Get("encerradas"), q.Get("de"))
	case q.Get("erro") == "propria-sessao":
		b.Error = "Essa é a sua sessão: para sair, use Sair."
	case q.Get("erro") == "sessao":
		b.Error = "Essa sessão já não existe; nada mudou."
	}
}
