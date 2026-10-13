package api_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPatchPendentesEAprovacao(t *testing.T) {
	h, st, log := ambientePaginas(t)
	item := "/painel/patch/item?fix=8.18.0-1ubuntu2.10&id=curl"

	b := pagina(h, "GET", "/painel/patch", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<span class="kpi-rot">Itens pendentes</span><span class="kpi-val">1</span><span class="kpi-sub">em 1 sistemas</span>`,
		`<span class="kpi-rot">Aguardando aprovação</span><span class="kpi-val v-laranja">1</span>`,
		`<a href="/painel/patch/item?fix=8.18.0-1ubuntu2.10&amp;id=curl"><code>curl</code></a>`, "USN-9002-1",
		`<span class="estado reboot_pendente">aguardando</span>`, `<span class="ic">🩹</span>Patch`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("lista: faltou %q", tr)
		}
	}

	// O item: sistemas afetados; leitura não aprova.
	b = pagina(h, "GET", item, sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `<a href="/painel/maquinas/60ef97e4-0001?aba=pendencias">vm-ubuntu</a>`) || !strings.Contains(b, "Aprovar exige o perfil admin") ||
		strings.Contains(b, `action="/painel/patch/aprovar"`) {
		t.Errorf("item, leitura:\n%s", b)
	}
	if r := pagina(h, "POST", "/painel/patch/aprovar", sessaoTeste, url.Values{"id": {"curl"}, "fix": {"8.18.0-1ubuntu2.10"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura aprovando: %d", r.Code)
	}

	// Admin aprova, com observação; a segunda vez não troca quem aprovou.
	aprova := func(form url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", "/painel/patch/aprovar", sessaoAdmin, form))
	}
	if l := aprova(url.Values{"id": {"curl"}, "fix": {"8.18.0-1ubuntu2.10"}, "nota": {"testado no piloto"}}); l != item+"&aprovado=1" {
		t.Fatalf("aprovar: %s", l)
	}
	if l := aprova(url.Values{"id": {"curl"}, "fix": {"8.18.0-1ubuntu2.10"}}); l != item+"&erro=ja-aprovado" {
		t.Errorf("de novo: %s", l)
	}
	// Item que não está pendente não é aprovado (nome qualquer, ou outra correção).
	if l := aprova(url.Values{"id": {"curl"}, "fix": {"9.9"}}); !strings.HasSuffix(l, "&erro=nao-pendente") {
		t.Errorf("correção que ninguém tem: %s", l)
	}
	if l := aprova(url.Values{"id": {"curl"}, "fix": {"8.18.0-1ubuntu2.10"}, "nota": {strings.Repeat("x", 501)}}); !strings.HasSuffix(l, "&erro=nota") {
		t.Errorf("observação longa: %s", l)
	}
	ap, _ := st.PatchApprovals(t.Context())
	if len(ap) != 1 || ap[0].ApprovedBy != "thyago" || ap[0].Note != "testado no piloto" {
		t.Fatalf("aprovação gravada: %+v", ap)
	}
	if !strings.Contains(log.String(), `msg="patch aprovado" item=curl fixed_in=8.18.0-1ubuntu2.10 username=thyago`) {
		t.Errorf("log:\n%s", log)
	}
	b = pagina(h, "GET", "/painel/patch?status=aprovados", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `<span class="estado em_dia">aprovado</span> <span class="muted">thyago · `) {
		t.Errorf("lista com a aprovação:\n%s", b)
	}
	b = pagina(h, "GET", item, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<dt>Observação</dt><dd>testado no piloto</dd>") || !strings.Contains(b, `action="/painel/patch/revogar"`) {
		t.Errorf("item aprovado:\n%s", b)
	}

	// Revogar volta a aguardar, e fica no log.
	if l := local(pagina(h, "POST", "/painel/patch/revogar", sessaoAdmin, url.Values{"id": {"curl"}, "fix": {"8.18.0-1ubuntu2.10"}})); l != item+"&revogado=1" {
		t.Errorf("revogar: %s", l)
	}
	if ap, _ := st.PatchApprovals(t.Context()); len(ap) != 0 || !strings.Contains(log.String(), "aprovação de patch revogada") {
		t.Errorf("revogada: %+v", ap)
	}
	if r := pagina(h, "GET", "/painel/patch/item?id=nao-existe", sessaoTeste, nil); r.Code != http.StatusNotFound {
		t.Errorf("item desconhecido: %d", r.Code)
	}
}
