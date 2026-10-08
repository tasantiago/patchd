package api_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// adFalso responde como o pacote ad: perfil, credenciais recusadas, sem grupo ou fora do ar.
type adFalso struct {
	fora     bool
	contas   map[string][2]string // usuário → senha, perfil ("" = sem grupo)
	chamadas int
}

func (f *adFalso) Authenticate(_ context.Context, user, pw string) (string, error) {
	f.chamadas++
	if f.fora {
		return "", errors.New("AD indisponível: dial: connect: connection refused")
	}
	c, ok := f.contas[user]
	if !ok || c[0] != pw {
		return "", fmt.Errorf("%w: senha inválida (52e)", panel.ErrBadCredentials)
	}
	if c[1] == "" {
		return "", panel.ErrNoRole
	}
	return c[1], nil
}

// ambienteAD: o usuário local admin.lab (senhaAdmin) e um AD com três contas.
func ambienteAD(t *testing.T) (http.Handler, *store.Memory, *adFalso, *syncBuffer) {
	t.Helper()
	st := store.NewMemory()
	hash, err := panel.HashPassword(senhaAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePanelUser(context.Background(), panel.User{Name: "admin.lab", Role: panel.RoleAdmin, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	dir := &adFalso{contas: map[string][2]string{
		"fulano":   {"senha-do-ad-1", panel.RoleAdmin},
		"ciclano":  {"senha-do-ad-2", panel.RoleRead},
		"beltrano": {"senha-do-ad-3", ""},
		// Mesmo nome e senha da conta local: com o AD no ar, quem decide é o AD.
		"admin.lab": {"outra-senha-no-ad", panel.RoleRead},
	}}
	log := &syncBuffer{}
	h := api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithDirectory(dir))
	return h, st, dir, log
}

func TestLoginPeloAD(t *testing.T) {
	h, st, _, log := ambienteAD(t)

	if r := comCookie(h, "GET", "/painel/entrar", nil); !strings.Contains(r.Body.String(), "usuário da rede, sem o domínio") {
		t.Error("com o AD ligado, o formulário orienta o nome")
	}
	for usuario, perfil := range map[string]string{"fulano": panel.RoleAdmin, "Ciclano": panel.RoleRead} {
		senha := map[string]string{"fulano": "senha-do-ad-1", "Ciclano": "senha-do-ad-2"}[usuario]
		r := entra(h, usuario, senha, "198.51.100.20")
		if r.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", usuario, r.Code)
		}
		c := cookieDaSessao(t, r)
		s, _, _ := st.PanelSession(context.Background(), identity.Hash(c.Value))
		if s.Source != panel.SourceAD || s.Role != perfil || s.Username != strings.ToLower(usuario) {
			t.Errorf("%s: sessão %+v", usuario, s)
		}
		if r := comCookie(h, "GET", "/painel/", c); !strings.Contains(r.Body.String(), strings.ToLower(usuario)+" (ad)") {
			t.Errorf("%s: painel %s", usuario, r.Body)
		}
	}

	// Senha errada no AD e conta sem grupo: a mesma resposta genérica; o motivo vai para o log.
	for _, c := range [][2]string{{"fulano", "errada-mesmo"}, {"beltrano", "senha-do-ad-3"}} {
		if r := entra(h, c[0], c[1], "198.51.100.21"); r.Code != http.StatusUnauthorized || !strings.Contains(r.Body.String(), "Usuário ou senha inválidos.") {
			t.Errorf("%s: %d", c[0], r.Code)
		}
	}
	l := log.String()
	if !strings.Contains(l, "senha inválida (52e)") || !strings.Contains(l, "nenhum grupo do painel") {
		t.Errorf("motivos no log:\n%s", l)
	}
}

func TestContaLocalSoComOADForaDoAr(t *testing.T) {
	h, _, dir, log := ambienteAD(t)

	// AD no ar: a senha local do admin.lab não vale (o AD diz que a senha é outra).
	if r := entra(h, "admin.lab", senhaAdmin, "198.51.100.30"); r.Code != http.StatusUnauthorized {
		t.Fatalf("conta local com o AD no ar: %d", r.Code)
	}
	// AD fora do ar: a conta local entra, e só ela.
	dir.fora = true
	r := entra(h, "admin.lab", senhaAdmin, "198.51.100.31")
	if r.Code != http.StatusSeeOther {
		t.Fatalf("contingência: %d", r.Code)
	}
	if r := comCookie(h, "GET", "/painel/", cookieDaSessao(t, r)); !strings.Contains(r.Body.String(), "admin.lab (local)") {
		t.Errorf("sessão local: %s", r.Body)
	}
	if r := entra(h, "fulano", "senha-do-ad-1", "198.51.100.31"); r.Code != http.StatusUnauthorized {
		t.Errorf("usuário só do AD com o AD fora: %d", r.Code)
	}
	l := log.String()
	if !strings.Contains(l, "AD indisponível: o login tenta a conta local") || !strings.Contains(l, "AD indisponível e conta local recusada") ||
		!strings.Contains(l, "source=local") {
		t.Errorf("log:\n%s", l)
	}
	// Bloqueado pelo limite: nem pergunta ao AD.
	dir.fora = false
	antes := dir.chamadas
	for i := 0; i < 6; i++ {
		entra(h, "fulano", "errada", "198.51.100.40")
	}
	if dir.chamadas-antes != 5 {
		t.Errorf("o limite deveria poupar o AD: %d chamadas", dir.chamadas-antes)
	}
}

func TestSemADSoContaLocal(t *testing.T) {
	h, _, _ := ambientePainel(t)
	if r := comCookie(h, "GET", "/painel/entrar", nil); strings.Contains(r.Body.String(), "usuário da rede") {
		t.Error("sem AD, sem a dica")
	}
}
