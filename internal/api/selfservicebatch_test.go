package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// tela: o formulário como o navegador manda, com todas as linhas da regra (apps) e os campos
// de cada uma; extra acrescenta (ou troca) campos.
func tela(linhas map[string]url.Values, extra url.Values) url.Values {
	f := url.Values{}
	for app, campos := range linhas {
		f.Add("apps", app)
		for k, v := range campos {
			f[k+"."+app] = v
		}
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

// Aula 18.8: a oferta em lote — salvar a tabela inteira, aplicar aos marcados ou a todos, tudo
// ou nada na conferência, só as linhas que mudaram na trilha.
func TestOfertaEmLote(t *testing.T) {
	m, _ := thirdparty.Default()
	h, st, _ := ambientePaginas(t, api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	envia := func(f url.Values) *http.Response {
		r := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, f)
		return r.Result()
	}
	ofertas := func() map[string]store.SelfServiceOffer {
		l, _ := st.SelfServiceOffers(ctx)
		out := map[string]store.SelfServiceOffer{}
		for _, o := range l {
			out[o.AppID] = o
		}
		return out
	}
	trilha := func(acao string) int {
		ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: acao})
		n := 0
		for _, e := range ev {
			if e.Action == acao {
				n++
			}
		}
		return n
	}

	// A tela: uma tabela, um botão de salvar e a barra de lote; nenhum formulário por linha.
	b := pagina(h, "GET", "/painel/configuracao?aba=autoatendimento", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="atualizar.7zip"`) || !strings.Contains(b, `name="sel" value="vscode"`) || !strings.Contains(b, `value="salvar">Salvar alterações`) ||
		!strings.Contains(b, `name="lote_aprovacao"`) || strings.Contains(b, `form="ss-`) {
		t.Fatalf("tela:\n%s", b)
	}

	// Salvar a tabela: duas linhas mudam (7-Zip e VS Code), o resto fica fora da loja.
	linhas := map[string]url.Values{
		"7zip":              {"atualizar": {"1"}, "instalar": {"1"}},
		"vscode":            {"instalar": {"1"}, "grupos": {"GG-Dev"}},
		"notepad-plus-plus": {},
	}
	r := envia(tela(linhas, url.Values{"modo": {"salvar"}}))
	if r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Location"), "lote=") {
		t.Fatalf("salvar: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	o := ofertas()
	if len(o) != 2 || !o["7zip"].CanInstall || o["vscode"].CanUpdate || strings.Join(o["vscode"].Groups, ",") != "GG-Dev" {
		t.Fatalf("ofertas: %+v", o)
	}
	if trilha("autoatendimento.oferta") != 2 || trilha("autoatendimento.oferta_lote") != 1 {
		t.Errorf("trilha do salvar: %d %d", trilha("autoatendimento.oferta"), trilha("autoatendimento.oferta_lote"))
	}
	if b := pagina(h, "GET", r.Header.Get("Location"), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Ofertas gravadas: ") ||
		!strings.Contains(b, "7-Zip") || !strings.Contains(b, "Visual Studio Code") {
		t.Errorf("aviso:\n%s", b)
	}

	// A mesma tela de novo: nada mudou, nada vai para a trilha.
	envia(tela(linhas, url.Values{"modo": {"salvar"}}))
	if trilha("autoatendimento.oferta") != 2 || trilha("autoatendimento.oferta_lote") != 1 {
		t.Error("salvar sem mudança gravou")
	}

	// Lote nos marcados: aprovação da TI e grupos trocados, o resto mantido.
	r = envia(tela(linhas, url.Values{"modo": {"lote"}, "sel": {"7zip", "notepad-plus-plus"}, "lote_aprovacao": {"1"},
		"lote_instalar": {"1"}, "lote_grupos_modo": {"trocar"}, "lote_grupos": {"GG-TI, GG-Suporte"}}))
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("lote: %d", r.StatusCode)
	}
	o = ofertas()
	z, n, v := o["7zip"], o["notepad-plus-plus"], o["vscode"]
	if !z.NeedsApproval || !z.CanUpdate || !z.CanInstall || strings.Join(z.Groups, ",") != "GG-TI,GG-Suporte" ||
		!n.CanInstall || n.CanUpdate || !n.NeedsApproval || v.NeedsApproval || strings.Join(v.Groups, ",") != "GG-Dev" {
		t.Fatalf("lote: %+v", o)
	}

	// Lote em todos: tirar o instalar de todos. Quem fica sem nada sai da loja.
	linhas = map[string]url.Values{
		"7zip":              {"atualizar": {"1"}, "instalar": {"1"}, "aprovacao": {"1"}, "grupos": {"GG-TI, GG-Suporte"}},
		"vscode":            {"instalar": {"1"}, "grupos": {"GG-Dev"}},
		"notepad-plus-plus": {"instalar": {"1"}, "aprovacao": {"1"}, "grupos": {"GG-TI, GG-Suporte"}},
	}
	envia(tela(linhas, url.Values{"modo": {"lote"}, "lote_alvo": {"todos"}, "lote_instalar": {"0"}}))
	o = ofertas()
	if len(o) != 1 || o["7zip"].CanInstall || !o["7zip"].CanUpdate {
		t.Fatalf("lote em todos: %+v", o)
	}
	if trilha("autoatendimento.remover") != 2 {
		t.Errorf("remoções na trilha: %d", trilha("autoatendimento.remover"))
	}

	// Tudo ou nada: um grupo inválido numa linha e nada é gravado; a página volta com o que
	// foi digitado e a linha marcada.
	antes := ofertas()
	ruim := map[string]url.Values{
		"7zip":   {"atualizar": {"1"}, "instalar": {"1"}},
		"vscode": {"instalar": {"1"}, "grupos": {"x*)(cn=*"}},
	}
	w := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, tela(ruim, url.Values{"modo": {"salvar"}, "sel": {"vscode"}}))
	b = w.Body.String()
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(b, "Grupos inválidos em Visual Studio Code. Nada foi gravado") ||
		!strings.Contains(b, `<tr class="ss-erro">`) || !strings.Contains(b, `value="x*)(cn=*"`) || !strings.Contains(b, `para o lote" checked><input type="hidden" name="apps" value="vscode">`) {
		t.Fatalf("grupo inválido: %d\n%s", w.Code, b)
	}
	if depois := ofertas(); len(depois) != len(antes) || depois["7zip"].CanInstall {
		t.Errorf("gravou com erro: %+v", depois)
	}

	// Lote sem escolha, lote sem programa, programa fora das regras, e só admin.
	if w := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, tela(linhas, url.Values{"modo": {"lote"}, "sel": {"7zip"}})); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "escolha o que aplicar") {
		t.Errorf("lote vazio: %d", w.Code)
	}
	if w := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, tela(linhas, url.Values{"modo": {"lote"}, "lote_instalar": {"1"}})); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "marque ao menos um programa") {
		t.Errorf("lote sem marcados: %d", w.Code)
	}
	if r := envia(tela(map[string]url.Values{"adobe-acrobat": {"atualizar": {"1"}}}, url.Values{"modo": {"salvar"}})); !strings.HasSuffix(r.Header.Get("Location"), "erro=programa") {
		t.Errorf("programa sem install: %s", r.Header.Get("Location"))
	}
	if w := pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoTeste, tela(linhas, url.Values{"modo": {"salvar"}})); w.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", w.Code)
	}
}
