package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

func formCampanha(kv ...string) url.Values {
	f := url.Values{"nome": {"busca de outubro"}, "tipo": {"rescan"}, "anel": {"1", "0"}, "observacao": {"24"}, "quando": {"prazo"}, "prazo": {"24"}}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(f, kv[i])
			continue
		}
		f[kv[i]] = strings.Split(kv[i+1], "|")
	}
	return f
}

func TestCriarCampanhaPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()

	if b := pagina(h, "GET", "/painel/campanhas", sessaoTeste, nil).Body.String(); strings.Contains(b, "Nova campanha") {
		t.Error("leitura não vê o formulário")
	}
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/campanhas/nova"`) || !strings.Contains(b, "1 (padrão) <span class=\"muted\">· 2 máq.") {
		t.Errorf("admin:\n%s", b)
	}

	// Erros voltam ao formulário (422) com o que foi digitado, e nada é criado.
	for nome, c := range map[string]struct {
		f   url.Values
		msg string
	}{
		"sem grupo de implantação":      {formCampanha("anel", ""), "Marque ao menos um grupo de implantação"},
		"grupo de implantação repetido": {formCampanha("anel", "0|0"), "cada um uma vez"},
		"grupo de implantação 10":       {formCampanha("anel", "10"), "Grupos de implantação de 0 a 9"},
		"tipo server_url":               {formCampanha("tipo", "server_url"), "Escolha o tipo"},
		"update sem fontes":             {formCampanha("tipo", "update"), "de 1 a 50 fontes"},
		"fonte estranha":                {formCampanha("tipo", "update", "fontes", "curl; rm -rf"), "Fontes: nomes de pacote fonte"},
		"observação fora":               {formCampanha("observacao", "5"), "observação da lista"},
		"prazo fora":                    {formCampanha("prazo", "2"), "prazo da lista"},
		"reenvios fora":                 {formCampanha("reenvios", "9"), "reenvios automáticos da lista"},
		"ligar com prazo":               {formCampanha("ligar", "1"), "Ligar pela rede vai com a janela"},
		"sem nome":                      {formCampanha("nome", "   "), "dê um nome"},
		"janela sem janela":             {formCampanha("quando", "janela"), "Grupo de implantação sem janela de manutenção: 0, 1"},
	} {
		r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, c.f)
		if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.msg) {
			t.Errorf("%s: %d\n%s", nome, r.Code, r.Body.String())
		}
	}
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("anel", "")); !strings.Contains(r.Body.String(), `value="busca de outubro"`) ||
		!strings.Contains(r.Body.String(), "thyago · AD · admin") {
		t.Error("o formulário volta preenchido")
	}

	// Conferência: nada criado ainda; os grupos de implantação em ordem crescente, com o aviso do grupo de implantação vazio.
	r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("anel", "3|0"))
	b = r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Conferir a campanha") || !strings.Contains(b, "O grupo de implantação 3 não tem máquinas") ||
		!strings.Contains(b, `<input type="hidden" name="anel" value="0"><input type="hidden" name="anel" value="3">`) ||
		!strings.Contains(b, `name="confirmar" value="1"`) || !strings.Contains(b, "thyago · AD · admin") {
		t.Errorf("conferência:\n%s", b)
	}
	if l, _ := st.Campaigns(ctx, 10); len(l) != 0 {
		t.Fatal("a conferência não cria")
	}
	// Corrigir volta ao formulário preenchido.
	f := formCampanha("anel", "3|0")
	f.Set("corrigir", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `name="anel" value="3" checked`) {
		t.Errorf("corrigir: %d", r.Code)
	}
	// Leitura e outra origem não criam.
	f = formCampanha()
	f.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoTeste, f); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f, "Sec-Fetch-Site", "cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("outra origem: %d", r.Code)
	}
	// Criar.
	r = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
	if r.Code != http.StatusSeeOther || local(r) != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %d %s", r.Code, local(r))
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.CreatedBy != "thyago" || len(c.Rings) != 2 || c.Rings[0] != 0 || c.Observe != 24*time.Hour || c.TTL != 24*time.Hour || c.UseWindow {
		t.Errorf("campanha: %+v", c)
	}
	if !strings.Contains(log.String(), "campanha criada pelo painel") {
		t.Error("log")
	}

	// Na janela, com os grupos de implantação com janela: passa.
	w := window.Window{Days: window.AllDays, Start: 12 * time.Hour, Duration: time.Hour, Location: time.UTC}
	_ = st.SetWindow(ctx, 0, w, "x")
	_ = st.SetWindow(ctx, 1, w, "x")
	f = formCampanha("quando", "janela")
	f.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f); local(r) != "/painel/campanhas/2?criada=1" {
		t.Errorf("na janela: %d %s", r.Code, local(r))
	}
}

func TestCampanhaSemChaveDeJobs(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	if b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "exige a chave de jobs") || strings.Contains(b, "Nova campanha") {
		t.Error("sem chave: aviso, sem formulário")
	}
	f := formCampanha()
	f.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f); r.Code != http.StatusConflict {
		t.Errorf("sem chave: %d", r.Code)
	}
}

func TestControlarCampanhaPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: "busca", Type: jobs.TypeRescan, Rings: []int{0, 1}, TTL: 24 * time.Hour, CreatedBy: "thyago"})
	det := "/painel/campanhas/1"
	if id != 1 {
		t.Fatal(id)
	}

	b := pagina(h, "GET", det, sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "Campanha 1: busca") || !strings.Contains(b, "ainda não liberado") || strings.Contains(b, "/pausar") {
		t.Errorf("leitura:\n%s", b)
	}
	if b := pagina(h, "GET", det, sessaoAdmin, nil).Body.String(); !strings.Contains(b, det+"/pausar") || !strings.Contains(b, det+"/cancelar") || strings.Contains(b, "/retomar") {
		t.Errorf("admin em andamento:\n%s", b)
	}
	for _, p := range []string{"/painel/campanhas/99", "/painel/campanhas/x", "/painel/campanhas/0"} {
		if r := pagina(h, "GET", p, sessaoAdmin, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	if r := pagina(h, "POST", det+"/pausar", sessaoTeste, url.Values{}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if r := pagina(h, "POST", det+"/apagar", sessaoAdmin, url.Values{}); r.Code != http.StatusNotFound {
		t.Errorf("operação desconhecida: %d", r.Code)
	}

	pede := func(op string) string {
		t.Helper()
		return local(pagina(h, "POST", det+"/"+op, sessaoAdmin, url.Values{}))
	}
	if l := pede("retomar"); l != det+"?erro=estado" {
		t.Errorf("retomar em andamento: %s", l)
	}
	if l := pede("pausar"); l != det+"?feito=pausar&jobs=0" {
		t.Errorf("pausar: %s", l)
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.State != campaign.StatePaused || c.Detail != "pausada por thyago" {
		t.Errorf("pausada: %+v", c)
	}
	if b := pagina(h, "GET", det+"?feito=pausar", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Campanha pausada") || !strings.Contains(b, det+"/retomar") {
		t.Errorf("depois de pausar:\n%s", b)
	}
	if l := pede("retomar"); l != det+"?feito=retomar&jobs=0" {
		t.Errorf("retomar: %s", l)
	}
	if l := pede("cancelar"); l != det+"?feito=cancelar&jobs=0" {
		t.Errorf("cancelar: %s", l)
	}
	if b := pagina(h, "GET", det, sessaoAdmin, nil).Body.String(); strings.Contains(b, "/pausar") || strings.Contains(b, "/cancelar\"") {
		t.Error("cancelada: sem botões")
	}
	if l := pede("cancelar"); l != det+"?erro=estado" {
		t.Errorf("cancelar de novo: %s", l)
	}
}

// Aula 16.4: a campanha do Windows pelo painel leva KBs, e a conferência avisa do KB sem
// nenhuma CVE aprovada.
func TestCampanhaDoWindowsPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	_, _ = st.ApprovePatch(ctx, store.PatchApproval{Item: "CVE-2026-1", FixedIn: "26300.9711 (KB5127648)", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})

	if b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="windows_update"`) {
		t.Error("faltou o tipo no formulário")
	}
	// Vindo do cartão da máquina: o KB já preenchido; KB fora do formato é ignorado.
	b := pagina(h, "GET", "/painel/campanhas?tipo=windows_update&kb=kb5127648", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `value="windows_update" checked`) || !strings.Contains(b, `value="KB5127648" placeholder`) || !strings.Contains(b, `value="Windows KB5127648"`) {
		t.Error("pré-preenchido")
	}
	if b := pagina(h, "GET", "/painel/campanhas?tipo=windows_update&kb=x%22", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="rescan" checked`) {
		t.Error("KB ruim não preenche")
	}
	for nome, c := range map[string]struct {
		f   url.Values
		msg string
	}{
		"sem KB":  {formCampanha("tipo", "windows_update"), "Informe os KBs"},
		"KB ruim": {formCampanha("tipo", "windows_update", "fontes", "KB51; x"), "KBs: como aparecem"},
	} {
		if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, c.f); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.msg) {
			t.Errorf("%s: %d", nome, r.Code)
		}
	}
	f := formCampanha("tipo", "windows_update", "fontes", "kb5127648, 5130001")
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	if !strings.Contains(b, "instalar atualizações do Windows · KBs: KB5127648, KB5130001") || !strings.Contains(b, "O KB5130001 não tem nenhuma CVE aprovada") ||
		strings.Contains(b, "O KB5127648 não tem") || !strings.Contains(b, "O job não reinicia") {
		t.Errorf("conferência:\n%s", b)
	}
	f.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f); local(r) != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %d %s", r.Code, local(r))
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.Type != jobs.TypeWinUpdate || strings.Join(c.Sources, ",") != "KB5127648,KB5130001" {
		t.Errorf("campanha: %+v", c)
	}
	if b := pagina(h, "GET", "/painel/campanhas/1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dt>KBs</dt><dd>KB5127648, KB5130001</dd>") {
		t.Error("detalhe: faltou KBs")
	}
}

// Aula 16.5: a campanha de reinício pelo painel leva o aviso; a conferência explica quem
// recebe e alerta quando não é na janela.
func TestCampanhaDeReinicioPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	if b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="reboot"`) ||
		!strings.Contains(b, `<option value="15" selected>15 min</option>`) {
		t.Error("formulário: faltou o tipo ou o aviso")
	}
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("tipo", "reboot", "aviso", "7")); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "aviso do reinício da lista") {
		t.Errorf("aviso fora da lista: %d", r.Code)
	}
	f := formCampanha("tipo", "reboot", "aviso", "30", "nome", "reinício de outubro")
	b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	if !strings.Contains(b, "reiniciar · onde houver reinício pendente, com aviso de 30 min") || !strings.Contains(b, "o job vira dispensado") ||
		!strings.Contains(b, "Sem janela, cada máquina reinicia assim que receber o job") || !strings.Contains(b, `name="aviso" value="30"`) ||
		strings.Contains(b, "<th>Hoje</th>") {
		t.Errorf("conferência:\n%s", b)
	}
	f.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f); local(r) != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %d %s", r.Code, local(r))
	}
	if c, _, _ := st.Campaign(ctx, 1); c.Type != jobs.TypeReboot || c.RebootDelay != 30*time.Minute {
		t.Errorf("campanha: %+v", c)
	}
	if b := pagina(h, "GET", "/painel/campanhas/1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dt>Reinício</dt><dd>onde houver reinício pendente, com aviso de 30 min</dd>") {
		t.Error("detalhe")
	}
}

// As campanhas de "tudo" pelo painel: todas as atualizações do sistema (sempre) e todos os
// programas aprovados (só com as regras de terceiros).
func TestCampanhasDeTudoPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `value="sistema_todas"`) || strings.Contains(b, `value="terceiros_todos"`) {
		t.Fatal("o formulário: sistema sempre; terceiros só com as regras")
	}
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("tipo", "terceiros_todos")); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("terceiros sem regras: %d", r.Code)
	}
	f := formCampanha("tipo", "sistema_todas", "nome", "tudo de novembro", "fontes", "curl")
	f.Set("confirmar", "1")
	r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
	if r.Code != http.StatusSeeOther {
		t.Fatalf("criar: %d\n%s", r.Code, r.Body.String())
	}
	c, _, _ := st.Campaign(context.Background(), 1)
	if c.Type != "sistema_todas" || len(c.Sources) != 0 || c.Name != "tudo de novembro" {
		t.Errorf("campanha: %+v", c)
	}
	b = pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "todas as atualizações do sistema aprovadas em Patch") {
		t.Errorf("a lista não mostra o alvo:\n%.3000s", b)
	}
}

// O detalhe da campanha com muitos jobs: contadores por estado, filtro por estado e máquina,
// páginas de 100, e só o grupo escolhido mostra a tabela.
func TestCampanhaFiltroEPaginas(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	id, err := st.CreateCampaign(ctx, campaign.Campaign{Name: "muitos", Type: jobs.TypeRescan, Rings: []int{0, 1}, TTL: time.Hour, CreatedBy: "t"})
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := st.Campaign(ctx, id)
	c.LaunchedIdx, c.CurrentIdx = 1, 1
	_, _ = st.SaveCampaign(ctx, c, campaign.StateRunning)
	agora := time.Now()
	for i := 0; i < 180; i++ {
		maq := "60ef97e4-0001"
		if i >= 150 {
			maq = "7aa604f7-0002"
		}
		jid, _ := st.NextJobID(ctx)
		if err := st.CreateJob(ctx, store.NewJob{ID: jid, MachineID: maq, Type: jobs.TypeRescan, Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "t", NotAfter: agora.Add(time.Hour), CampaignID: id, CampaignIdx: 1}); err != nil {
			t.Fatal(err)
		}
		if i%18 == 0 {
			_, _ = st.FinishJob(ctx, jid, jobs.StateDone, "ok", agora)
		}
	}
	base := fmt.Sprintf("/painel/campanhas/%d", id)
	b := pagina(h, "GET", base, sessaoTeste, nil).Body.String()
	for _, quer := range []string{"pendente</span> <b>170</b>", "concluído</span> <b>10</b>", "1–100 de 180", "próxima ›", `name="q"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("página: falta %q", quer)
		}
	}
	if n := strings.Count(b, `<td class="num">`); n != 100 {
		t.Errorf("%d linhas na primeira página", n)
	}
	b = pagina(h, "GET", base+"?grupo=1&pagina=2", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "101–180 de 180") || !strings.Contains(b, "‹ anterior") {
		t.Error("segunda página")
	}
	b = pagina(h, "GET", base+"?grupo=1&estado=concluido", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "1–10 de 10") || !strings.Contains(b, `class="chip ativo"`) {
		t.Error("filtro por estado")
	}
	b = pagina(h, "GET", base+"?grupo=1&q=7aa6", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "1–30 de 30") {
		t.Error("filtro por máquina")
	}
	// O grupo 0 (sem jobs) não mostra tabela nem filtro.
	b = pagina(h, "GET", base+"?grupo=0", sessaoTeste, nil).Body.String()
	if strings.Contains(b, "1–100 de 180") || !strings.Contains(b, "ver os jobs") {
		t.Error("o grupo 0 escolhido não mostra os jobs do 1")
	}
}

// Reenviar o que não deu certo: o cartão aparece na campanha pausada com falhas no grupo atual,
// com a contagem por motivo; o pedido volta a campanha a andar e fica na auditoria.
func TestReenviarFalhasPeloPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: "reiniciar", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, CreatedBy: "t", AutoResend: 2})
	c, _, _ := st.Campaign(ctx, id)
	c.LaunchedIdx, c.State = 0, campaign.StatePaused
	_, _ = st.SaveCampaign(ctx, c, campaign.StateRunning)
	agora := time.Now()
	for _, maq := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		jid, _ := st.NextJobID(ctx)
		if err := st.CreateJob(ctx, store.NewJob{ID: jid, MachineID: maq, Type: jobs.TypeRescan, Signed: protocol.SignedJob{Payload: "{}", Signature: "s"},
			CreatedBy: "t", NotAfter: agora.Add(time.Hour), CampaignID: id, CampaignIdx: 0}); err != nil {
			t.Fatal(err)
		}
		_, _ = st.FinishJob(ctx, jid, jobs.StateRejected, "x", agora)
	}
	det := fmt.Sprintf("/painel/campanhas/%d", id)
	b := pagina(h, "GET", det, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Reenviar o que não deu certo", `name="motivo" value="rejeitado" checked`, "2 job(s) recusados pelo agente",
		"até 2 vez(es) por máquina"} {
		if !strings.Contains(b, quer) {
			t.Errorf("página: falta %q", quer)
		}
	}
	if b := pagina(h, "GET", det, sessaoTeste, nil).Body.String(); strings.Contains(b, "Reenviar o que não deu certo") {
		t.Error("leitura vê o reenvio")
	}
	if r := pagina(h, "POST", det+"/reenviar", sessaoTeste, url.Values{"motivo": {"rejeitado"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura reenvia: %d", r.Code)
	}
	if l := local(pagina(h, "POST", det+"/reenviar", sessaoAdmin, url.Values{})); l != det+"?erro=reenvio-vazio" {
		t.Errorf("vazio: %s", l)
	}
	if r := pagina(h, "POST", det+"/reenviar", sessaoAdmin, url.Values{"motivo": {"cancelado"}}); r.Code != http.StatusBadRequest {
		t.Errorf("motivo fora da lista: %d", r.Code)
	}
	if l := local(pagina(h, "POST", det+"/reenviar", sessaoAdmin, url.Values{"motivo": {"rejeitado"}})); l != det+"?feito=reenviar" {
		t.Fatalf("reenviar: %s", l)
	}
	if c, _, _ := st.Campaign(ctx, id); c.State != campaign.StateRunning || !strings.Contains(c.Detail, "reenvio pedido por thyago (rejeitado)") {
		t.Errorf("campanha: %+v", c)
	}
	if r, _ := st.TakeResend(ctx, id); len(r) != 1 || r[0] != "rejeitado" {
		t.Errorf("pedido guardado: %v", r)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "campanha.reenviar"}); len(ev) != 1 || ev[0].Detail != "motivos: rejeitado" {
		t.Errorf("trilha: %+v", ev)
	}
}

// Arquivar e excluir: a encerrada sai da lista (sozinha ou todas de uma vez) e vai para as
// Arquivadas, de onde volta ou é excluída com a confirmação; os jobs ficam.
func TestArquivarEExcluirCampanha(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	nova := func(nome, estado string) int64 {
		id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: nome, Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, CreatedBy: "t"})
		c, _, _ := st.Campaign(ctx, id)
		c.State = estado
		_, _ = st.SaveCampaign(ctx, c, campaign.StateRunning)
		return id
	}
	andando, feita, cancelada := nova("andando", campaign.StateRunning), nova("feita", campaign.StateDone), nova("cancelada", campaign.StateCanceled)
	det := func(id int64) string { return fmt.Sprintf("/painel/campanhas/%d", id) }

	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Arquivar as encerradas (2)") || strings.Contains(b, "Arquivadas (") {
		t.Errorf("lista: falta o botão")
	}
	if b := pagina(h, "GET", det(andando), sessaoAdmin, nil).Body.String(); strings.Contains(b, "/arquivar") {
		t.Error("a em andamento não se arquiva")
	}
	if l := local(pagina(h, "POST", det(feita)+"/arquivar", sessaoAdmin, nil)); l != fmt.Sprintf("/painel/campanhas?arquivada=%d", feita) {
		t.Fatalf("arquivar: %s", l)
	}
	if l := local(pagina(h, "POST", det(andando)+"/arquivar", sessaoAdmin, nil)); l != det(andando)+"?erro=estado" {
		t.Errorf("arquivar a em andamento: %s", l)
	}
	b = pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if strings.Contains(b, ">feita</a>") || !strings.Contains(b, "Arquivadas (1)") || !strings.Contains(b, "Arquivar as encerradas (1)") {
		t.Errorf("depois de arquivar:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/campanhas/arquivar-encerradas", sessaoAdmin, nil)); l != "/painel/campanhas?arquivadas-agora=1" {
		t.Errorf("todas: %s", l)
	}
	b = pagina(h, "GET", "/painel/campanhas?arquivadas=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">feita</a>") || !strings.Contains(b, ">cancelada</a>") || strings.Contains(b, ">andando</a>") {
		t.Errorf("arquivadas:\n%s", b)
	}
	if b := pagina(h, "GET", det(feita), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Excluir de vez") || !strings.Contains(b, "/desarquivar") {
		t.Error("página da arquivada")
	}
	if l := local(pagina(h, "POST", det(feita)+"/excluir", sessaoAdmin, nil)); l != det(feita)+"?erro=confirmar" {
		t.Errorf("sem confirmar: %s", l)
	}
	if r := pagina(h, "POST", det(feita)+"/excluir", sessaoTeste, url.Values{"confirmo": {"1"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura exclui: %d", r.Code)
	}
	if l := local(pagina(h, "POST", det(feita)+"/excluir", sessaoAdmin, url.Values{"confirmo": {"1"}})); l != fmt.Sprintf("/painel/campanhas?arquivadas=1&excluida=%d", feita) {
		t.Fatalf("excluir: %s", l)
	}
	if _, found, _ := st.Campaign(ctx, feita); found {
		t.Error("ficou")
	}
	if l := local(pagina(h, "POST", det(cancelada)+"/desarquivar", sessaoAdmin, nil)); l != det(cancelada)+"?feito=desarquivar" {
		t.Errorf("desarquivar: %s", l)
	}
	for _, acao := range []string{"campanha.arquivar", "campanha.arquivar-encerradas", "campanha.excluir", "campanha.desarquivar"} {
		if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: acao}); len(ev) != 1 {
			t.Errorf("trilha %s: %d", acao, len(ev))
		}
	}
}
