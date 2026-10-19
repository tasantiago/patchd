package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
)

func TestJanelasPeloPainel(t *testing.T) {
	h, st, log := ambientePaginas(t, api.WithTimezone("America/Porto_Velho"))
	ctx := context.Background()

	// Leitura vê a tabela, sem formulário; o admin vê o formulário com o fuso sugerido.
	b := pagina(h, "GET", "/painel/aneis", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "<h1>Automação <span class=\"muted\">› Anéis e janelas</span></h1>") || !strings.Contains(b, "1 (padrão)") || strings.Contains(b, `action="/painel/aneis/janela"`) {
		t.Errorf("leitura:\n%s", b)
	}
	b = pagina(h, "GET", "/painel/aneis", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/aneis/janela"`) || !strings.Contains(b, `value="America/Porto_Velho"`) {
		t.Errorf("admin:\n%s", b)
	}

	janela := url.Values{"anel": {"0"}, "dia": {"seg", "sex"}, "inicio": {"22:00"}, "duracao": {"360"}, "fuso": {"America/Porto_Velho"}}
	// Leitura e outra origem: 403, nada gravado.
	if r := pagina(h, "POST", "/painel/aneis/janela", sessaoTeste, janela); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/aneis/janela", sessaoAdmin, janela, "Sec-Fetch-Site", "cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("outra origem: %d", r.Code)
	}
	if w, _ := st.Windows(ctx); len(w) != 0 {
		t.Fatal("nada pode ter sido gravado")
	}

	// Formulários ruins: volta com o erro certo, nada gravado.
	for campo, ruins := range map[string][]string{
		"anel": {"10", "-1", "x"}, "dia": {"segunda", ""}, "inicio": {"25:00", "9:5"}, "duracao": {"45", "0", "2000"},
		"fuso": {"Local", "../etc/passwd", "Marte/Base", "America/Porto Velho"},
	} {
		for _, v := range ruins {
			f := url.Values{}
			for k, vs := range janela {
				f[k] = vs
			}
			f[campo] = []string{v}
			if v == "" {
				delete(f, campo)
			}
			r := pagina(h, "POST", "/painel/aneis/janela", sessaoAdmin, f)
			quer := map[string]string{"anel": "erro=anel", "dia": "erro=dias", "inicio": "erro=inicio", "duracao": "erro=duracao", "fuso": "erro=fuso"}[campo]
			if r.Code != http.StatusSeeOther || !strings.Contains(local(r), quer) {
				t.Errorf("%s=%q: %d %s", campo, v, r.Code, local(r))
			}
		}
	}
	if w, _ := st.Windows(ctx); len(w) != 0 {
		t.Fatal("formulário ruim gravou janela")
	}
	if b := pagina(h, "GET", "/painel/aneis?erro=fuso", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Fuso horário desconhecido") {
		t.Error("mensagem do fuso")
	}

	// O admin grava: a janela passa da meia-noite, com quem definiu.
	r := pagina(h, "POST", "/painel/aneis/janela", sessaoAdmin, janela)
	if r.Code != http.StatusSeeOther || local(r) != "/painel/aneis?janela=1&anel=0" {
		t.Fatalf("gravar: %d %s", r.Code, local(r))
	}
	w, _ := st.Windows(ctx)
	if len(w) != 1 || w[0].Ring != 0 || w[0].UpdatedBy != "thyago" || !strings.Contains(w[0].Window.String(), "22:00-04:00 do dia seguinte (America/Porto_Velho)") {
		t.Fatalf("janela: %+v", w)
	}
	if !strings.Contains(log.String(), "janela do anel definida pelo painel") {
		t.Error("log da janela")
	}
	b = pagina(h, "GET", local(r), sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Janela do anel 0 gravada") || !strings.Contains(b, "0 (piloto)") || !strings.Contains(b, `action="/painel/aneis/0/remover-janela"`) {
		t.Errorf("depois de gravar:\n%s", b)
	}

	// Remover; remover de novo diz que não havia.
	if r := pagina(h, "POST", "/painel/aneis/0/remover-janela", sessaoAdmin, url.Values{}); local(r) != "/painel/aneis?removida=1&anel=0" {
		t.Errorf("remover: %s", local(r))
	}
	if r := pagina(h, "POST", "/painel/aneis/0/remover-janela", sessaoAdmin, url.Values{}); local(r) != "/painel/aneis?erro=sem-janela&anel=0" {
		t.Errorf("remover de novo: %s", local(r))
	}
	if r := pagina(h, "POST", "/painel/aneis/99/remover-janela", sessaoAdmin, url.Values{}); !strings.Contains(local(r), "erro=anel") {
		t.Errorf("anel inválido: %s", local(r))
	}
}

func TestAnelDaMaquinaPeloPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"

	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, "/anel\"") {
		t.Error("leitura não vê o formulário do anel")
	}
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/anel"`) ||
		!strings.Contains(b, `<option value="1" selected>`) {
		t.Errorf("admin, anel 1 marcado:\n%s", b)
	}
	if r := pagina(h, "POST", maq+"/anel", sessaoTeste, url.Values{"anel": {"0"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	r := pagina(h, "POST", maq+"/anel", sessaoAdmin, url.Values{"anel": {"0"}})
	if r.Code != http.StatusSeeOther || local(r) != maq+"?anel-alterado=1&anel=0" {
		t.Fatalf("mudar: %d %s", r.Code, local(r))
	}
	if ring, _, _ := st.MachineRing(ctx, "60ef97e4-0001"); ring != 0 {
		t.Errorf("anel gravado: %d", ring)
	}
	if b := pagina(h, "GET", local(r), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Máquina no anel 0.") || !strings.Contains(b, "0 (piloto)") {
		t.Errorf("depois:\n%s", b)
	}
	if r := pagina(h, "POST", maq+"/anel", sessaoAdmin, url.Values{"anel": {"12"}}); local(r) != maq+"?erro=anel" {
		t.Errorf("anel 12: %s", local(r))
	}
	if r := pagina(h, "POST", "/painel/maquinas/nao-existe-0009/anel", sessaoAdmin, url.Values{"anel": {"2"}}); r.Code != http.StatusNotFound {
		t.Errorf("máquina inexistente: %d", r.Code)
	}
}

func TestAnelEmLoteNaFrota(t *testing.T) {
	h, st, log := ambientePaginas(t)
	ctx := context.Background()

	if b := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String(); strings.Contains(b, `name="maquina"`) || !strings.Contains(b, "<th>Anel</th>") {
		t.Errorf("leitura: coluna sim, caixas não")
	}
	if b := pagina(h, "GET", "/painel/?estado=faltando", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/aneis/maquinas"`) ||
		!strings.Contains(b, `name="estado" value="faltando"`) || !strings.Contains(b, `name="maquina" value="60ef97e4-0001"`) {
		t.Errorf("admin:\n%s", b)
	}
	lote := url.Values{"maquina": {"60ef97e4-0001", "7aa604f7-0002", "nao-existe-0009", "../x"}, "anel": {"3"}, "estado": {"faltando"}}
	if r := pagina(h, "POST", "/painel/aneis/maquinas", sessaoTeste, lote); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	r := pagina(h, "POST", "/painel/aneis/maquinas", sessaoAdmin, lote)
	if r.Code != http.StatusSeeOther || local(r) != "/painel/?estado=faltando&movidas=2&ignoradas=2&anel=3" {
		t.Fatalf("lote: %d %s", r.Code, local(r))
	}
	for _, id := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		if ring, _, _ := st.MachineRing(ctx, id); ring != 3 {
			t.Errorf("%s: anel %d", id, ring)
		}
	}
	b := pagina(h, "GET", local(r), sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "2 máquina(s) no anel 3. 2 ignorada(s)") || !strings.Contains(b, `<td class="num">3</td>`) {
		t.Errorf("frota depois:\n%s", b)
	}
	if !strings.Contains(log.String(), "anel alterado pelo painel em lote") {
		t.Error("log do lote")
	}
	// Filtro que não é estado conhecido não volta na URL; nenhuma marcada é erro.
	r = pagina(h, "POST", "/painel/aneis/maquinas", sessaoAdmin, url.Values{"anel": {"2"}, "estado": {"x&admin=1"}})
	if local(r) != "/painel/?erro=nenhuma" {
		t.Errorf("sem marcadas: %s", local(r))
	}
}

// Aula 15.11: Campanhas, Jobs e Anéis são abas da Automação. Aula 19.1: na barra lateral, cada um
// tem o seu item no grupo AUTOMAÇÃO, e só o da página fica em destaque.
func TestMenuAutomacao(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	for path, c := range map[string]struct{ menu, aba string }{
		"/painel/campanhas": {`<a href="/painel/campanhas" title="Campanhas" class="ativo" aria-current="page">`, `<a href="/painel/campanhas" class="ativa" aria-current="page">Campanhas</a>`},
		"/painel/jobs":      {`<a href="/painel/jobs" title="Jobs" class="ativo" aria-current="page">`, `<a href="/painel/jobs" class="ativa" aria-current="page">Jobs</a>`},
		"/painel/aneis":     {`<a href="/painel/aneis" title="Anéis e janelas" class="ativo" aria-current="page">`, `<a href="/painel/aneis" class="ativa" aria-current="page">Anéis e janelas</a>`},
	} {
		b := pagina(h, "GET", path, sessaoAdmin, nil).Body.String()
		if !strings.Contains(b, c.menu) || !strings.Contains(b, c.aba) || strings.Count(b, `class="ativa"`) != 1 || strings.Count(b, `class="ativo"`) != 1 {
			t.Errorf("%s: menu ou aba", path)
		}
		if !strings.Contains(b, `<p class="grupo"><span class="rot">Automação</span></p>`) {
			t.Errorf("%s: sem o grupo Automação", path)
		}
	}
	b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoAdmin, nil).Body.String()
	if strings.Count(b, `class="ativo"`) != 1 || !strings.Contains(b, `<a href="/painel/campanhas" title="Campanhas" >`) {
		t.Error("fora da Automação, o item do menu aparece sem destaque")
	}
}
