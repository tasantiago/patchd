package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/oidc/oidctest"
	"github.com/tasantiago/patchd/internal/panel"
)

var senhaGerada = regexp.MustCompile(`<pre class="comando">([A-Za-z0-9]{20})</pre>`)

// Configuração › Acesso (Aula 15.3): usuários locais criados e trocados com senha gerada,
// mostrada uma vez; as travas da própria conta e do último admin local.
func TestUsuariosLocaisPeloPainel(t *testing.T) {
	h, st, log := ambientePaginas(t)
	ctx := context.Background()
	aba := "/painel/configuracao?aba=acesso"

	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Nenhum usuário local: se o AD cair", "o login do painel (sem AD nem RHBK)", "Sem PATCHD_LDAP_HOST", "Sem PATCHD_OIDC_ISSUER"} {
		if !strings.Contains(b, tr) {
			t.Errorf("vazio: faltou %q", tr)
		}
	}
	if r := pagina(h, "POST", "/painel/configuracao/acesso/usuarios", sessaoTeste, url.Values{"nome": {"x1"}, "perfil": {"admin"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura cria: %d", r.Code)
	}

	// Criar: a senha aparece uma vez, passa na política e confere com o hash gravado.
	r := pagina(h, "POST", "/painel/configuracao/acesso/usuarios", sessaoAdmin, url.Values{"nome": {" Contingencia.SEAT "}, "perfil": {"admin"}})
	m := senhaGerada.FindStringSubmatch(r.Body.String())
	if r.Code != http.StatusOK || m == nil || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Body.String(), "Usuário local criado: contingencia.seat") {
		t.Fatalf("criar: %d %v", r.Code, m)
	}
	u, ok, _ := st.PanelUser(ctx, "contingencia.seat")
	if !ok || u.Role != panel.RoleAdmin || !panel.CheckPassword(u.PasswordHash, m[1]) || panel.CheckPasswordPolicy(m[1]) != nil {
		t.Errorf("gravado: %+v", u)
	}
	if strings.Contains(log.String(), m[1]) || !strings.Contains(log.String(), `msg="usuário local criado pelo painel" local_user=contingencia.seat role=admin username=thyago`) {
		t.Errorf("log:\n%s", log)
	}
	if r := pagina(h, "POST", "/painel/configuracao/acesso/usuarios", sessaoAdmin, url.Values{"nome": {"contingencia.seat"}, "perfil": {"leitura"}}); r.Code != http.StatusConflict {
		t.Errorf("repetido: %d", r.Code)
	}
	for nome, f := range map[string]url.Values{"nome": {"nome": {"A B"}, "perfil": {"admin"}}, "perfil": {"nome": {"ana"}, "perfil": {"root"}}} {
		if r := pagina(h, "POST", "/painel/configuracao/acesso/usuarios", sessaoAdmin, f); r.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", nome, r.Code)
		}
	}

	// O único admin local não sai; com um leitor, a lista mostra as ações.
	pagina(h, "POST", "/painel/configuracao/acesso/usuarios", sessaoAdmin, url.Values{"nome": {"leitor"}, "perfil": {"leitura"}})
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "único admin local") || !strings.Contains(b, `action="/painel/configuracao/acesso/usuarios/leitor/remover"`) {
		t.Errorf("lista:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/configuracao/acesso/usuarios/contingencia.seat/remover", sessaoAdmin, url.Values{})); l != aba+"&erro=ultimo-admin" {
		t.Errorf("último admin: %s", l)
	}

	// Nova senha: outra senha, a anterior deixa de valer e as sessões locais caem.
	antes, _, _ := st.PanelUser(ctx, "leitor")
	sessaoLeitor := panel.SessionPrefix + "sessao-do-leitor"
	agora := time.Now().UTC()
	st.CreatePanelSession(ctx, identity.Hash(sessaoLeitor), panel.Session{Username: "leitor", Role: panel.RoleRead, Source: panel.SourceLocal,
		CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	r = pagina(h, "POST", "/painel/configuracao/acesso/usuarios/leitor/senha", sessaoAdmin, url.Values{})
	m2 := senhaGerada.FindStringSubmatch(r.Body.String())
	depois, _, _ := st.PanelUser(ctx, "leitor")
	if m2 == nil || depois.PasswordHash == antes.PasswordHash || !panel.CheckPassword(depois.PasswordHash, m2[1]) {
		t.Errorf("nova senha: %v", m2)
	}
	if _, ok, _ := st.PanelSession(ctx, identity.Hash(sessaoLeitor)); ok {
		t.Error("a sessão local do leitor continuou")
	}

	// A própria conta local não muda por aqui.
	sessaoLocal := panel.SessionPrefix + "sessao-local-admin"
	st.CreatePanelSession(ctx, identity.Hash(sessaoLocal), panel.Session{Username: "contingencia.seat", Role: panel.RoleAdmin, Source: panel.SourceLocal,
		CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	if l := local(pagina(h, "POST", "/painel/configuracao/acesso/usuarios/contingencia.seat/senha", sessaoLocal, url.Values{})); l != aba+"&erro=propria" {
		t.Errorf("própria: %s", l)
	}
	if b := pagina(h, "GET", aba, sessaoLocal, nil).Body.String(); !strings.Contains(b, "<code>contingencia.seat</code> <span class=\"muted\">(você)</span>") {
		t.Error("a lista marca a conta da sessão")
	}

	// Remover o leitor; um nome que não existe.
	if l := local(pagina(h, "POST", "/painel/configuracao/acesso/usuarios/leitor/remover", sessaoAdmin, url.Values{})); l != aba+"&removido=leitor" {
		t.Errorf("remover: %s", l)
	}
	if _, ok, _ := st.PanelUser(ctx, "leitor"); ok {
		t.Error("o leitor continuou")
	}
	if l := local(pagina(h, "POST", "/painel/configuracao/acesso/usuarios/ninguem/senha", sessaoAdmin, url.Values{})); l != aba+"&erro=usuario" {
		t.Errorf("inexistente: %s", l)
	}
}

// O teste do AD (com um verificador falso) e o da descoberta do RHBK.
func TestTestesDeLogin(t *testing.T) {
	p := oidctest.New(t)
	prov, err := oidc.New(oidc.Config{Issuer: p.Issuer(), ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "https://127.0.0.1:18443/painel/oidc/retorno", RolesClaim: "groups", AdminRole: "patchd-admin", ReadRole: "patchd-leitura"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(_ context.Context, user, pw string) (api.ADCheckResult, error) {
		switch user {
		case "fora":
			return api.ADCheckResult{Unavailable: "tempo esgotado na porta 636"}, nil
		case "quebra":
			return api.ADCheckResult{}, errors.New("resposta inesperada")
		}
		if pw != "certa" {
			return api.ADCheckResult{TLSVersion: "TLS 1.3", BadPassword: true}, nil
		}
		return api.ADCheckResult{TLSVersion: "TLS 1.3", AdminSet: true, ReadSet: true, Admin: true, Role: panel.RoleAdmin}, nil
	}
	h, _, log := ambientePaginas(t, api.WithOIDC(prov), api.WithADInfo(api.ADInfo{Check: check,
		Vars: []api.ADVar{{Name: "PATCHD_LDAP_HOST", Set: true}, {Name: "PATCHD_LDAP_BASE_DN", Note: "a base é derivada do domínio"}}}))

	b := pagina(h, "GET", "/painel/configuracao?aba=acesso", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"<dt><code>PATCHD_LDAP_HOST</code></dt><dd>definida</dd>", "<dt><code>PATCHD_LDAP_BASE_DN</code></dt><dd>vazia <span class=\"muted\">· a base é derivada do domínio</span></dd>",
		"<dt>Emissor</dt><dd><code>" + p.Issuer() + "</code></dd>", "o formulário de senha; o RHBK entra pelo botão"} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	testa := func(user, pw string) string {
		return pagina(h, "POST", "/painel/configuracao/acesso/testar-ad", sessaoAdmin, url.Values{"usuario": {user}, "senha": {pw}}).Body.String()
	}
	b = testa("fulano", "certa")
	for _, tr := range []string{"Resultado para fulano", "<dt>Conexão LDAPS</dt><dd>ok (TLS 1.3)</dd>", "<dt>Senha</dt><dd>conferida pelo AD</dd>",
		"<dt>Grupo admin (PATCHD_LDAP_ADMIN_GROUP)</dt><dd>membro</dd>", `<dt>Grupo leitura (PATCHD_LDAP_READ_GROUP)</dt><dd class="nivel-atencao">não membro</dd>`,
		"<dt>Perfil no painel</dt><dd>admin</dd>"} {
		if !strings.Contains(b, tr) {
			t.Errorf("ok: faltou %q", tr)
		}
	}
	if b := testa("fora", "x"); !strings.Contains(b, `<dd class="nivel-erro">falhou: tempo esgotado na porta 636</dd>`) {
		t.Error("indisponível")
	}
	if b := testa("quebra", "x"); !strings.Contains(b, "O teste falhou: resposta inesperada") {
		t.Error("erro")
	}
	if strings.Contains(log.String(), "certa") || !strings.Contains(log.String(), `msg="teste de login no AD pelo painel" tested_user=fulano ok=true role=admin username=thyago`) {
		t.Errorf("log:\n%s", log)
	}
	// Senha errada conta no limite do login: na sexta, bloqueado.
	for i := 0; i < 5; i++ {
		if b := testa("beltrano", "errada"); !strings.Contains(b, `<dt>Senha</dt><dd class="nivel-erro">recusada pelo AD</dd>`) {
			t.Fatalf("tentativa %d", i+1)
		}
	}
	if r := pagina(h, "POST", "/painel/configuracao/acesso/testar-ad", sessaoAdmin, url.Values{"usuario": {"beltrano"}, "senha": {"certa"}}); r.Code != http.StatusTooManyRequests {
		t.Errorf("bloqueio: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/configuracao/acesso/testar-ad", sessaoTeste, url.Values{"usuario": {"fulano"}, "senha": {"certa"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura testa: %d", r.Code)
	}

	// RHBK: a descoberta responde.
	if b := pagina(h, "POST", "/painel/configuracao/acesso/testar-oidc", sessaoAdmin, url.Values{}).Body.String(); !strings.Contains(b, `<p class="aviso ok">Descoberta conferida`) {
		t.Errorf("oidc:\n%s", b)
	}
	p.Close()
	if b := pagina(h, "POST", "/painel/configuracao/acesso/testar-oidc", sessaoAdmin, url.Values{}).Body.String(); !strings.Contains(b, `<p class="aviso erro">descoberta do provedor:`) {
		t.Error("oidc fora do ar")
	}
}
