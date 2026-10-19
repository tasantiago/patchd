package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Patch › Visão geral (Aula 15.1): os indicadores, o que pede atenção, os anéis, os
// sistemas e a execução dos últimos 7 dias, das mesmas fontes das outras telas.
func TestVisaoGeralDePatch(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	st.SetMachineRing(ctx, "60ef97e4-0001", 0)

	b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`href="/painel/patch?aba=geral" title="Patch › Visão geral" class="ativo"`, `<a href="/painel/patch?aba=geral" class="ativa" aria-current="page">Visão geral</a>`,
		`<span class="kpi-rot">Máquinas em dia</span><span class="kpi-val v-verde">33%</span><span class="kpi-sub">1 de 3</span>`,
		`<span class="kpi-rot">Sistema: correções aguardando</span><span class="kpi-val v-vermelho">1</span><span class="kpi-sub">1 item(ns) (1 pacote) em 1 máquina(s) · 1 com exploração conhecida</span>`,
		`<li class="erro"><a href="/painel/patch?status=exploradas">1 item(ns) com exploração conhecida aguardando aprovação</a></li>`,
		"<td>0 (piloto)</td><td class=\"num\">1</td><td class=\"num\">0</td><td class=\"num\">1</td>",
		"<td>Ubuntu</td>", "<td>Windows</td>", "<td>sem inventário</td>",
		"Nenhum job nos últimos 7 dias.",
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}

	// Uma campanha pausada e um job sem sucesso entram na atenção e na execução.
	id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: "curl", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, CreatedBy: "thyago"})
	c, _, _ := st.Campaign(ctx, id)
	c.State, c.Detail = campaign.StatePaused, "anel 0: 1 job(s) sem sucesso"
	st.SaveCampaign(ctx, c, campaign.StateRunning)
	rec, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: "60ef97e4-0001", Type: jobs.TypeRescan, TTL: time.Hour, By: "thyago"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	st.FinishJob(ctx, rec.ID, jobs.StateFailed, "x", time.Now().UTC())
	b = pagina(h, "GET", "/painel/patch?aba=geral", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<a href="/painel/campanhas/1">Campanha 1 (curl) pausada: anel 0: 1 job(s) sem sucesso</a>`,
		`<a href="/painel/jobs">1 job(s) sem sucesso nas últimas 24 h</a>`,
		"<td>buscar atualizações</td><td class=\"num\">1</td><td class=\"num\">0</td><td class=\"num\">1</td>",
		`<span class="kpi-sub">1 pausada(s)</span>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("depois: faltou %q", tr)
		}
	}
	// A aba SO continua em /painel/patch.
	if b := pagina(h, "GET", "/painel/patch", sessaoTeste, nil).Body.String(); !strings.Contains(b, "Patch <span class=\"muted\">› Sistema operacional</span>") {
		t.Error("aba do sistema")
	}
}

// Aula 15.7: as pendências do sistema por correção. Uma atualização acumulada do Windows
// fecha dezenas de CVEs; o cartão conta correções, e uma máquina que concentra quase tudo
// vira aviso.
func TestCorrecoesAguardando(t *testing.T) {
	agora := time.Now().UTC()
	var win1, win2 []protocol.PendingItem
	for i := range 60 {
		win1 = append(win1, protocol.PendingItem{ID: fmt.Sprintf("CVE-2026-%04d", 1000+i), FixedIn: "26200.9500", Severity: "Important", Exploited: i < 2})
	}
	for i := range 6 {
		win2 = append(win2, protocol.PendingItem{ID: fmt.Sprintf("CVE-2026-%04d", 2000+i), FixedIn: "26100.7000", Severity: "Important"})
	}
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Counts: map[string]int{"faltando": 3},
			Machines: []protocol.ComplianceStatus{
				{MachineID: "a-0001", Hostname: "pc-esquecido", OS: "Windows 11 Pro 25H2", State: "faltando", Pending: 60, PendingUnit: "CVEs", EvaluatedAt: agora},
				{MachineID: "b-0002", Hostname: "pc-em-dia", OS: "Windows 11 Pro 24H2", State: "faltando", Pending: 6, PendingUnit: "CVEs", EvaluatedAt: agora},
				{MachineID: "c-0003", Hostname: "vm-ubuntu", OS: "Ubuntu 26.04", State: "faltando", Pending: 3, PendingUnit: "pacotes", EvaluatedAt: agora},
			}},
		itens: map[string][]protocol.PendingItem{"a-0001": win1, "b-0002": win2, "c-0003": {
			{ID: "curl", FixedIn: "8.18.0-1ubuntu2.10"}, {ID: "openssl", FixedIn: "3.5.0-2ubuntu1.3"}, {ID: "vim", FixedIn: "2:9.1-1ubuntu1.2"}}},
	}
	h, st, _ := ambientePaginas(t, api.WithCompliance(src))
	// Uma CVE já aprovada não conta.
	st.ApprovePatch(context.Background(), store.PatchApproval{Item: "CVE-2026-2005", FixedIn: "26100.7000", ApprovedBy: "thyago", ApprovedAt: agora})

	b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<span class="kpi-rot">Sistema: correções aguardando</span><span class="kpi-val v-vermelho">5</span><span class="kpi-sub">68 item(ns) (65 CVEs, 3 pacotes) em 3 máquina(s) · 2 com exploração conhecida</span>`,
		"<tr><td><code>26200.9500</code></td><td>atualização do sistema</td><td>Windows</td><td class=\"num\">60</td>\n" +
			`<td class="num"><span class="expl">2</span></td><td class="num">1</td><td class="nowrap"><a href="/painel/patch?q=26200.9500&amp;status=aguardando">Ver e aprovar</a></td></tr>`,
		`<tr><td><code>26100.7000</code></td><td>atualização do sistema</td><td>Windows</td><td class="num">5</td>`,
		`<td>pacote curl</td><td>Ubuntu</td><td class="num">1</td>`,
		`<li class="atencao"><a href="/painel/maquinas/a-0001?aba=pendencias">pc-esquecido concentra 60 dos 68 itens aguardando (88%): atualizar essa máquina muda o retrato da frota</a></li>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	// A ordem: a correção que fecha mais itens primeiro.
	if strings.Index(b, "26200.9500") > strings.Index(b, "26100.7000") || strings.Index(b, "26100.7000") > strings.Index(b, "pacote curl") {
		t.Error("ordem das correções")
	}
	// O link filtra a lista do sistema por aquela correção.
	l := pagina(h, "GET", "/painel/patch?q=26200.9500&status=aguardando", sessaoTeste, nil).Body.String()
	if !strings.Contains(l, "CVE-2026-1000") || strings.Contains(l, "CVE-2026-2000") {
		t.Error("o filtro pela correção")
	}
}

// Aula 15.9: aprovar a correção inteira, com conferência por resumo e impressão digital.
func TestAprovarCorrecao(t *testing.T) {
	agora := time.Now().UTC()
	var win []protocol.PendingItem
	for i := range 260 {
		sev := "Important"
		if i%10 == 0 {
			sev = "Critical"
		}
		win = append(win, protocol.PendingItem{ID: fmt.Sprintf("CVE-2026-%05d", 10000+i), FixedIn: "26300.9711 (KB5127648)", Severity: sev, Exploited: i == 7})
	}
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Counts: map[string]int{"faltando": 2},
			Machines: []protocol.ComplianceStatus{
				{MachineID: "a-0001", Hostname: "vm-26h2", OS: "Windows 11 Pro 26H2", State: "faltando", Pending: 260, PendingUnit: "CVEs", EvaluatedAt: agora},
				{MachineID: "c-0003", Hostname: "vm-ubuntu", OS: "Ubuntu 26.04", State: "faltando", Pending: 2, PendingUnit: "pacotes", EvaluatedAt: agora},
			}},
		itens: map[string][]protocol.PendingItem{"a-0001": win, "c-0003": {
			// Dois pacotes sem relação com o mesmo texto de versão: nunca uma "correção".
			{ID: "curl", FixedIn: "1.0-1"}, {ID: "vim", FixedIn: "1.0-1"}}},
	}
	h, st, log := ambientePaginas(t, api.WithCompliance(src))
	ctx := context.Background()
	fix := "26300.9711 (KB5127648)"

	// A visão geral oferece o botão ao admin, só nas linhas de CVE.
	b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoAdmin, nil).Body.String()
	if strings.Count(b, `action="/painel/patch/correcao"`) != 1 || !strings.Contains(b, `<input type="hidden" name="fix" value="26300.9711 (KB5127648)">`) {
		t.Errorf("botão na visão geral:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoTeste, nil).Body.String(); strings.Contains(b, "/painel/patch/correcao") {
		t.Error("o perfil leitura não vê o botão")
	}
	// A lista filtrada pela correção também oferece.
	if b := pagina(h, "GET", "/painel/patch?status=aguardando&q=26300.9711", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Os 260 itens aguardando deste filtro são CVEs da mesma correção") {
		t.Error("aviso na lista filtrada")
	}
	if b := pagina(h, "GET", "/painel/patch?status=aguardando&q=1.0-1", sessaoAdmin, nil).Body.String(); strings.Contains(b, "Conferir a correção inteira") {
		t.Error("pacotes não viram correção inteira")
	}
	if r := pagina(h, "POST", "/painel/patch/correcao", sessaoTeste, url.Values{"fix": {fix}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}

	// Conferência: o resumo, a amostra de 20 e a impressão digital.
	b = pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {fix}}).Body.String()
	for _, tr := range []string{"260 CVEs serão aprovadas, 1 com exploração conhecida, em 1 máquina(s) (Windows)",
		"<dt>Critical</dt><dd>26</dd>", "<dt>Important</dt><dd>234</dd>", "E mais 240:", "<code>CVE-2026-10007</code>"} {
		if !strings.Contains(b, tr) {
			t.Errorf("conferência: faltou %q", tr)
		}
	}
	m := regexp.MustCompile(`name="conferido" value="([0-9a-f]{32})"`).FindStringSubmatch(b)
	if m == nil {
		t.Fatal("sem a impressão digital")
	}
	if l, _ := st.PatchApprovals(ctx); len(l) != 0 {
		t.Fatal("a conferência não aprova nada")
	}

	// Impressão digital de outra lista: volta para a conferência.
	b = pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {fix}, "confirmar": {"1"}, "conferido": {"0000"}}).Body.String()
	if !strings.Contains(b, "A lista desta correção mudou desde a conferência") {
		t.Error("impressão digital diferente")
	}
	// Uma CVE aprovada por outra pessoa entre a conferência e o clique também muda a lista.
	st.ApprovePatch(ctx, store.PatchApproval{Item: "CVE-2026-10259", FixedIn: fix, ApprovedBy: "bia", ApprovedAt: agora})
	b = pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {fix}, "confirmar": {"1"}, "conferido": {m[1]}}).Body.String()
	if !strings.Contains(b, "mudou desde a conferência") || !strings.Contains(b, "259 CVEs serão aprovadas") {
		t.Error("lista mudada pela aprovação de outra pessoa")
	}
	m = regexp.MustCompile(`name="conferido" value="([0-9a-f]{32})"`).FindStringSubmatch(b)

	// Aprovar: tudo numa vez, com a observação; uma linha no log.
	r := pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {fix}, "confirmar": {"1"}, "conferido": {m[1]}, "nota": {"testado no piloto"}})
	if l := local(r); l != "/painel/patch?ja=0&lote=259&q=26300.9711+%28KB5127648%29&status=aprovados" {
		t.Errorf("depois de aprovar: %d %s", r.Code, l)
	}
	l, _ := st.PatchApprovals(ctx)
	if len(l) != 260 {
		t.Fatalf("aprovações: %d", len(l))
	}
	for _, ap := range l {
		if ap.Item != "CVE-2026-10259" && (ap.ApprovedBy != "thyago" || ap.Note != "testado no piloto") {
			t.Fatalf("aprovação: %+v", ap)
		}
	}
	if !strings.Contains(log.String(), `msg="correção aprovada pelo painel" fixed_in="26300.9711 (KB5127648)" aprovados=259 ja_aprovados=0 exploradas=1`) ||
		strings.Count(log.String(), "patch aprovado") != 0 {
		t.Errorf("log:\n%s", log)
	}
	if b := pagina(h, "GET", local(r), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "259 itens aprovados em lote.") {
		t.Error("aviso depois de aprovar")
	}
	// De novo: nada aguardando.
	if l := local(pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {fix}})); !strings.Contains(l, "erro=lote-vazio") {
		t.Errorf("sem nada aguardando: %s", l)
	}
	// Os pacotes de mesma versão não são aprovados pela rota da correção.
	if l := local(pagina(h, "POST", "/painel/patch/correcao", sessaoAdmin, url.Values{"fix": {"1.0-1"}})); !strings.Contains(l, "erro=lote-vazio") {
		t.Errorf("pacotes: %s", l)
	}
}
