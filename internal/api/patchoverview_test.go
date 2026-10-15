package api_test

import (
	"context"
	"fmt"
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
		`href="/painel/patch?aba=geral" class="ativo"`, `<a href="/painel/patch?aba=geral" class="ativa" aria-current="page">Visão geral</a>`,
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
			`<td class="num"><span class="expl">2</span></td><td class="num">1</td><td><a href="/painel/patch?q=26200.9500&amp;status=aguardando">Ver e aprovar</a></td></tr>`,
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
