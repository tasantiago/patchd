package api_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

// Aula 15.13: as ações do painel, os logins e as exportações vão para a trilha, com quem,
// de onde e o request_id do log.
func TestTrilhaDeAuditoria(t *testing.T) {
	h, st, _ := ambientePainel(t)
	ctx := context.Background()

	if r := entra(h, "thyago", "errada", "198.51.100.7"); r.Code != http.StatusUnauthorized {
		t.Fatalf("senha errada: %d", r.Code)
	}
	r := entra(h, "thyago", senhaAdmin, "198.51.100.7")
	c := cookieDaSessao(t, r)
	if r := comCookie(h, "GET", "/painel/inventario.csv", c); r.Code != http.StatusOK {
		t.Fatalf("CSV: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/configuracao/acesso/usuarios", c.Value, url.Values{"nome": {"contingencia.lab"}, "perfil": {"leitura"}}); r.Code != http.StatusOK {
		t.Fatalf("criar usuário: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/sair", c.Value, url.Values{}); r.Code != http.StatusSeeOther {
		t.Fatalf("sair: %d", r.Code)
	}

	l, err := st.AuditEvents(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var acoes []string
	for _, e := range l {
		acoes = append(acoes, e.Action)
	}
	quer := []string{"sessao.sair", "usuario.criar", "exportar.csv", "login.ok", "login.recusado"}
	if len(acoes) != len(quer) {
		t.Fatalf("ações: %v", acoes)
	}
	for i := range quer {
		if acoes[i] != quer[i] {
			t.Fatalf("ações: %v, quer %v", acoes, quer)
		}
	}
	ok, criou, recusado := l[3], l[1], l[4]
	if ok.Username != "thyago" || ok.Source != "local" || ok.Remote != "198.51.100.7" || ok.RequestID == "" || ok.Detail != "perfil admin" {
		t.Errorf("login: %+v", ok)
	}
	if criou.Target != "contingencia.lab" || criou.Detail != "perfil leitura" {
		t.Errorf("usuário criado: %+v", criou)
	}
	if recusado.Username != "thyago" || recusado.Source != "" || recusado.Detail == "" {
		t.Errorf("recusado: %+v", recusado)
	}
	if l[2].Target != "/painel/inventario.csv" {
		t.Errorf("exportação: %+v", l[2])
	}
}
