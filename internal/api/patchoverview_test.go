package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
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
		`<span class="kpi-rot">Sistema: aguardando aprovação</span><span class="kpi-val v-vermelho">1</span><span class="kpi-sub">1 com exploração conhecida · 1 item(ns) pendente(s) no total</span>`,
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
