package api_test

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

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

// Aula 15.14: a aba Auditoria (só admin), com filtro, páginas de 50 e CSV.
func TestAbaAuditoria(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	agora := time.Now().UTC()
	for i := range 60 {
		e := store.AuditEvent{At: agora.Add(-time.Duration(60-i) * time.Minute), Username: "ana", Source: "ad", Action: "patch.aprovar",
			Target: fmt.Sprintf("CVE-2026-%04d", i), Remote: "192.0.2.10", RequestID: fmt.Sprintf("req%02d", i)}
		if i%10 == 0 {
			e.Username, e.Action, e.Target, e.Detail = "bia", "login.recusado", "", "=HYPERLINK(1)"
		}
		st.RecordAudit(ctx, e)
	}
	st.RecordAudit(ctx, store.AuditEvent{At: agora.Add(-10 * 24 * time.Hour), Username: "velho", Action: "login.ok"})
	aba := "/painel/configuracao?aba=auditoria"

	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if n := strings.Count(b, "<tr><td class=\"nowrap\">"); n != 50 {
		t.Errorf("primeira página: %d linhas", n)
	}
	for _, tr := range []string{`class="ativa" aria-current="page">Auditoria</a>`, "<code>CVE-2026-0059</code>", "<code>req59</code>",
		`<span class="expl">Login recusado</span>`, "mais antigos ›", `href="/painel/configuracao/auditoria.csv"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	if strings.Contains(b, "velho") {
		t.Error("o padrão é 7 dias")
	}
	next := regexp.MustCompile(`href="(/painel/configuracao\?[^"]*antes=[0-9]+[^"]*)">mais antigos`).FindStringSubmatch(b)
	if next == nil {
		t.Fatal("sem o link da página seguinte")
	}
	b = pagina(h, "GET", strings.ReplaceAll(next[1], "&amp;", "&"), sessaoAdmin, nil).Body.String()
	if n := strings.Count(b, "<tr><td class=\"nowrap\">"); n != 10 || !strings.Contains(b, "« mais novos") || strings.Contains(b, "mais antigos ›") {
		t.Errorf("segunda página: %d linhas", n)
	}
	if b := pagina(h, "GET", aba+"&usuario=bia&acao=login.", sessaoAdmin, nil).Body.String(); strings.Count(b, "<tr><td class=\"nowrap\">") != 6 {
		t.Error("filtro por usuário e grupo")
	}
	if b := pagina(h, "GET", aba+"&periodo=tudo&usuario=velho", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<code>velho</code>") {
		t.Error("período tudo")
	}
	// Leitura não vê, e a tentativa fica na trilha.
	if r := pagina(h, "GET", aba, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if l, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "acesso.negado"}); len(l) != 1 || l[0].Target != "/painel/configuracao" {
		t.Errorf("acesso negado: %+v", l)
	}

	// CSV: o filtro inteiro (sem página), com a proteção contra fórmula, e a exportação na trilha.
	r := pagina(h, "GET", "/painel/configuracao/auditoria.csv?acao=login.recusado", sessaoAdmin, nil)
	if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("CSV: %d", r.Code)
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(r.Body.String(), "\xef\xbb\xbf")))
	cr.Comma = ';'
	rows, err := cr.ReadAll()
	if err != nil || len(rows) != 7 || rows[0][4] != "acao" || rows[1][7] != "'=HYPERLINK(1)" || rows[1][8] != "192.0.2.10" {
		t.Errorf("CSV: %v %v", rows, err)
	}
	if l, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "exportar.csv"}); len(l) != 1 || l[0].Username != "thyago" || l[0].Target != "/painel/configuracao/auditoria.csv" {
		t.Errorf("exportação: %+v", l)
	}
}
