package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAjudaExigeSessaoERespeitaPerfil(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	if r := pagina(h, "GET", "/painel/ajuda", "", nil); r.Code != http.StatusSeeOther {
		t.Fatalf("ajuda sem sessão: %d", r.Code)
	}
	leitura := pagina(h, "GET", "/painel/ajuda", sessaoTeste, nil).Body.String()
	if !strings.Contains(leitura, "Seu perfil: Leitura") || !strings.Contains(leitura, "Acompanhar execuções") ||
		strings.Contains(leitura, `href="/painel/configuracao`) || strings.Contains(leitura, `href="/painel/scripts`) {
		t.Error("a ajuda de leitura deve orientar a consulta sem oferecer tarefas administrativas")
	}
	admin := pagina(h, "GET", "/painel/ajuda", sessaoAdmin, nil).Body.String()
	if !strings.Contains(admin, "Adicionar computadores ao servidor") || !strings.Contains(admin, `href="/painel/configuracao?aba=agente"`) {
		t.Error("a ajuda do administrador deve incluir o registro dos agentes")
	}
}

func TestBuscaDaAjudaNaoDependeDeJargaoOuAcento(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	for _, c := range []struct{ query, found string }{
		{"jobs", "Acompanhar execuções e investigar uma falha"},
		{"localizacoes", "Identificar prédios, andares e salas"},
		{"LOCALIZAÇÕES", "Identificar prédios, andares e salas"},
		{"instalar programas", "Aprovar e instalar atualizações"},
	} {
		b := pagina(h, "GET", "/painel/ajuda?q="+url.QueryEscape(c.query), sessaoAdmin, nil).Body.String()
		if !strings.Contains(b, c.found) || strings.Contains(b, "Nenhum assunto encontrado.") {
			t.Errorf("busca %q sem o assunto esperado", c.query)
		}
	}
	q := `<script>alert(1)</script>`
	b := pagina(h, "GET", "/painel/ajuda?q="+url.QueryEscape(q), sessaoAdmin, nil).Body.String()
	if strings.Contains(b, "<script>") || !strings.Contains(b, "&lt;script&gt;") || !strings.Contains(b, "Nenhum assunto encontrado.") {
		t.Error("busca sem escape ou sem orientação para nenhum resultado")
	}
	if b := pagina(h, "GET", "/painel/ajuda?q=adicionar", sessaoTeste, nil).Body.String(); strings.Contains(b, "Adicionar computadores ao servidor") {
		t.Error("a busca expôs uma tarefa administrativa ao perfil de leitura")
	}
}

func TestNavegacaoContextualMantemFerramentaEConfiguracaoSelecionadas(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	b := pagina(h, "GET", "/painel/", sessaoAdmin, nil).Body.String()
	if strings.Contains(b, `<details class="nav-tools" open>`) || strings.Contains(b, `title="Patch › Sistema operacional"`) ||
		!strings.Contains(b, `href="/painel/ajuda#computadores"`) {
		t.Error("a entrada deve reduzir os atalhos repetidos e oferecer ajuda de contexto")
	}
	b = pagina(h, "GET", "/painel/scripts", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<details class="nav-tools" open>`) || !strings.Contains(b, `href="/painel/scripts" class="ativo"`) {
		t.Error("a ferramenta selecionada não pode ficar escondida no menu recolhido")
	}
	b = pagina(h, "GET", "/painel/configuracao?aba=auditoria", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<details class="settings-more" open>`) || !strings.Contains(b, `aria-current="page">Histórico de alterações`) {
		t.Error("um favorito de configuração avançada deve abrir o grupo e indicar a aba ativa")
	}
}
