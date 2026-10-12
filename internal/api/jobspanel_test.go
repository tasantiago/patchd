package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/window"
)

func local(r interface{ Header() http.Header }) string { return r.Header().Get("Location") }

func TestBuscarAgoraPeloPainel(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"

	// O admin vê o botão; o perfil leitura, não.
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/buscar"`) ||
		!strings.Contains(b, "Nenhum job para esta máquina") {
		t.Errorf("admin:\n%s", b)
	}
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, "/buscar") {
		t.Error("o perfil leitura não vê o botão")
	}

	// Leitura e outra origem: 403, e nenhum job.
	if r := pagina(h, "POST", maq+"/buscar", sessaoTeste, url.Values{}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if r := pagina(h, "POST", maq+"/buscar", sessaoAdmin, url.Values{}, "Sec-Fetch-Site", "cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("outra origem: %d", r.Code)
	}
	if l, _ := st.Jobs(ctx, "", 10); len(l) != 0 {
		t.Fatal("nenhuma das tentativas acima cria job")
	}

	// O admin pede: um job rescan assinado, com o usuário como autor e prazo de 3 h.
	r := pagina(h, "POST", maq+"/buscar", sessaoAdmin, url.Values{})
	if r.Code != http.StatusSeeOther || local(r) != maq+"?criado=1&job=1" {
		t.Fatalf("buscar: %d %s", r.Code, local(r))
	}
	l, _ := st.Jobs(ctx, "60ef97e4-0001", 10)
	if len(l) != 1 || l[0].State != jobs.StatePending || l[0].CreatedBy != "thyago" || l[0].Type != jobs.TypeRescan {
		t.Fatalf("job: %+v", l)
	}
	env, _ := st.DeliverJobs(ctx, "60ef97e4-0001", time.Now().UTC())
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	if j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", time.Now().UTC(), 0); err != nil || j.NotAfter.Sub(j.IssuedAt) != 3*time.Hour {
		t.Errorf("assinatura e prazo: %+v %v", j, err)
	}
	if !strings.Contains(log.String(), "job criado pelo painel") {
		t.Error("a criação vai para o log")
	}

	// Segundo clique com o job em aberto (entregue): nada novo, e a tela diz qual job.
	r = pagina(h, "POST", maq+"/buscar", sessaoAdmin, url.Values{})
	if local(r) != maq+"?erro=job-aberto&job=1" {
		t.Errorf("segundo clique: %s", local(r))
	}
	if l, _ := st.Jobs(ctx, "", 10); len(l) != 1 {
		t.Error("o segundo clique não cria job")
	}
	b := pagina(h, "GET", maq+"?erro=job-aberto&job=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "ainda em aberto para esta máquina (job 1)") || !strings.Contains(b, `<span class="job entregue">entregue</span>`) ||
		strings.Contains(b, "/cancelar") {
		t.Errorf("detalhe com job entregue (sem cancelar):\n%s", b)
	}

	// O job encerra; um novo pedido vira o job 2, pendente e cancelável.
	if ok, _ := st.FinishJob(ctx, 1, jobs.StateDone, "busca nova recebida", time.Now().UTC()); !ok {
		t.Fatal("encerrar")
	}
	if r := pagina(h, "POST", maq+"/buscar", sessaoAdmin, url.Values{}); local(r) != maq+"?criado=1&job=2" {
		t.Fatalf("novo pedido: %s", local(r))
	}
	b = pagina(h, "GET", maq+"?criado=1&job=2", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Job 2 criado", `<span class="job concluido">concluído</span>`, `action="/painel/jobs/2/cancelar"`,
		`name="maquina" value="60ef97e4-0001"`, "buscar atualizações"} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}

	// Lista da frota, para o perfil leitura: os dois jobs, com a máquina e sem cancelar.
	b = pagina(h, "GET", "/painel/jobs", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `<a href="/painel/maquinas/60ef97e4-0001"><code>60ef97e4</code></a>`) || !strings.Contains(b, "<td>vm</td>") ||
		strings.Count(b, `<td class="num">`) != 2 || strings.Contains(b, "/cancelar") {
		t.Errorf("lista (leitura):\n%s", b)
	}

	// Cancelar: volta para a máquina; de novo, erro; um destino estranho cai na lista.
	r = pagina(h, "POST", "/painel/jobs/2/cancelar", sessaoAdmin, url.Values{"maquina": {"60ef97e4-0001"}})
	if local(r) != maq+"?cancelado=1&job=2" {
		t.Errorf("cancelar: %s", local(r))
	}
	if l, _ := st.Jobs(ctx, "", 1); l[0].State != jobs.StateCanceled || l[0].Detail != "cancelado por thyago" {
		t.Errorf("cancelado: %+v", l[0])
	}
	if r := pagina(h, "POST", "/painel/jobs/2/cancelar", sessaoAdmin, url.Values{"maquina": {"//outro.site/x"}}); local(r) != "/painel/jobs?erro=cancelar&job=2" {
		t.Errorf("cancelar de novo: %s", local(r))
	}
	if r := pagina(h, "POST", "/painel/jobs/2/cancelar", sessaoTeste, url.Values{}); r.Code != http.StatusForbidden {
		t.Errorf("leitura cancela: %d", r.Code)
	}

	// Máquina aposentada: nada é criado.
	if _, err := st.RetireMachine(ctx, "7aa604f7-0002", "antiga", "thyago"); err != nil {
		t.Fatal(err)
	}
	if r := pagina(h, "POST", "/painel/maquinas/7aa604f7-0002/buscar", sessaoAdmin, url.Values{}); local(r) != "/painel/maquinas/7aa604f7-0002" {
		t.Errorf("aposentada: %s", local(r))
	}
	if l, _ := st.Jobs(ctx, "7aa604f7-0002", 10); len(l) != 0 {
		t.Error("job para aposentada")
	}
}

func TestPainelSemChaveDeJobs(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	maq := "/painel/maquinas/60ef97e4-0001"
	b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String()
	if strings.Contains(b, "/buscar") || !strings.Contains(b, "PATCHD_JOB_SIGNING_KEY_FILE") {
		t.Errorf("sem chave, sem botão e com a explicação:\n%s", b)
	}
	if r := pagina(h, "POST", maq+"/buscar", sessaoAdmin, url.Values{}); local(r) != maq+"?erro=sem-chave" {
		t.Errorf("sem chave: %s", local(r))
	}
	if l, _ := st.Jobs(context.Background(), "", 10); len(l) != 0 {
		t.Error("sem chave, nenhum job")
	}
}

func TestAnelEJanelaNoDetalhe(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); !strings.Contains(b, "<dt>Anel</dt><dd>1</dd>") || !strings.Contains(b, "jobs com janela não podem") {
		t.Errorf("anel padrão, sem janela:\n%s", b)
	}
	if ok, _ := st.SetMachineRing(ctx, "60ef97e4-0001", 0); !ok {
		t.Fatal("anel")
	}
	pvh, _ := time.LoadLocation("America/Porto_Velho")
	if err := st.SetWindow(ctx, 0, window.Window{Days: window.AllDays, Start: 3 * time.Hour, Duration: 10 * time.Minute, Location: pvh}, "t"); err != nil {
		t.Fatal(err)
	}
	// Um job com janela daqui a 3 h aparece como "abre em 3 h".
	inicio := time.Now().UTC().Add(3 * time.Hour)
	if _, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: "60ef97e4-0001", Type: jobs.TypeRescan, TTL: time.Hour, By: "t", NotBefore: inicio}, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String()
	for _, tr := range []string{"<dt>Anel</dt><dd>0 (piloto)</dd>", "todos 03:00-03:10 (America/Porto_Velho)", "<td>abre em 2 h 59 min</td>"} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
}

func TestAtualizarPeloPainel(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"

	b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/atualizar"`) || !strings.Contains(b, `name="item" value="curl"`) ||
		!strings.Contains(b, `title="curl, libcurl4t64"`) || strings.Contains(b, `value="janela"`) {
		t.Errorf("admin, sem janela:\n%s", b)
	}
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, `name="item"`) {
		t.Error("leitura não marca pendências")
	}
	pede := func(form url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", maq+"/atualizar", sessaoAdmin, form))
	}
	if r := pagina(h, "POST", maq+"/atualizar", sessaoTeste, url.Values{"item": {"curl"}, "quando": {"agora"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	if l := pede(url.Values{"quando": {"agora"}}); l != maq+"?erro=nada-marcado" {
		t.Errorf("nada marcado: %s", l)
	}
	if l := pede(url.Values{"item": {"bash"}, "quando": {"agora"}}); l != maq+"?erro=nada-marcado" {
		t.Errorf("item que não está pendente: %s", l)
	}
	if l := pede(url.Values{"item": {"curl"}, "quando": {"janela"}}); l != maq+"?erro=sem-janela" {
		t.Errorf("sem janela: %s", l)
	}

	// Agora: os pacotes e a versão vêm da avaliação do servidor, não do formulário.
	if l := pede(url.Values{"item": {"curl"}, "quando": {"agora"}, "pacote": {"bash"}}); l != maq+"?criado=1&job=1" {
		t.Fatalf("atualizar: %s", l)
	}
	env, _ := st.DeliverJobs(ctx, "60ef97e4-0001", time.Now().UTC())
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", time.Now().UTC(), 0)
	p, _ := jobs.ParseUpdateParams(j.Params)
	if err != nil || j.Type != jobs.TypeUpdate || len(p.Packages) != 2 || p.Packages[1] != (jobs.Package{Name: "libcurl4t64", MinVersion: "8.18.0-1ubuntu2.10"}) {
		t.Fatalf("job: %+v %+v %v", j, p, err)
	}
	if !strings.Contains(log.String(), "packages=2") {
		t.Error("log")
	}
	if l := pede(url.Values{"item": {"curl"}, "quando": {"agora"}}); l != maq+"?erro=update-aberto&job=1" {
		t.Errorf("segundo pedido: %s", l)
	}

	// Na janela do anel: o job leva o início da janela.
	_, _ = st.FinishJob(ctx, 1, jobs.StateDone, "x", time.Now().UTC())
	pvh, _ := time.LoadLocation("America/Porto_Velho")
	_ = st.SetWindow(ctx, 1, window.Window{Days: window.AllDays, Start: 3 * time.Hour, Duration: time.Hour, Location: pvh}, "t")
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="janela"`) {
		t.Error("com janela, a opção aparece")
	}
	if l := pede(url.Values{"item": {"curl"}, "quando": {"janela"}}); l != maq+"?criado=1&job=2" {
		t.Fatalf("janela: %s", l)
	}
	if l, _ := st.Jobs(ctx, "60ef97e4-0001", 1); l[0].NotBefore == nil {
		t.Error("o job na janela leva o not_before")
	}
}

func TestPaginaDeCampanhas(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	if b := pagina(h, "GET", "/painel/campanhas", sessaoTeste, nil).Body.String(); !strings.Contains(b, "Nenhuma campanha") {
		t.Errorf("vazia:\n%s", b)
	}
	_, _ = st.CreateCampaign(ctx, campaign.Campaign{Name: `<b>bluez</b>`, Type: jobs.TypeUpdate, Sources: []string{"bluez"}, Rings: []int{0, 1},
		UseWindow: true, CreatedBy: "thyago"})
	b := pagina(h, "GET", "/painel/campanhas", sessaoTeste, nil).Body.String()
	for _, tr := range []string{"&lt;b&gt;bluez&lt;/b&gt;", "atualizar pacotes, na janela", "<code>[0] → 1</code>", `<span class="job em_andamento">em andamento</span>`, "thyago"} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
}

func TestReiniciarPeloPainel(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); strings.Contains(b, "Reiniciar com aviso") {
		t.Error("sem reinício pendente, sem botão")
	}
	// A máquina vira Linux com reinício pendente na última busca.
	sim := true
	_, _ = st.SaveInventory(ctx, "60ef97e4-0001", protocol.InventoryReport{SchemaVersion: 1, Hash: "h2", OS: protocol.OSInfo{Family: "linux", Hostname: "vm"}})
	_ = st.SaveScan(ctx, "60ef97e4-0001", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, Reboot: &protocol.RebootStatus{Pending: &sim}})

	b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/reiniciar"`) || !strings.Contains(b, `<option value="15" selected>15 min</option>`) {
		t.Errorf("admin:\n%s", b)
	}
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, "/reiniciar") {
		t.Error("leitura não reinicia")
	}
	pede := func(path string, form url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", path, sessaoAdmin, form))
	}
	if l := pede(maq+"/reiniciar", url.Values{"aviso": {"7"}, "quando": {"agora"}}); l != maq+"?erro=reinicio-form" {
		t.Errorf("aviso fora da lista: %s", l)
	}
	if l := pede(maq+"/reiniciar", url.Values{"aviso": {"15"}, "quando": {"agora"}}); l != maq+"?criado=1&job=1" {
		t.Fatalf("reiniciar: %s", l)
	}
	env, _ := st.DeliverJobs(ctx, "60ef97e4-0001", time.Now().UTC())
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", time.Now().UTC(), 0)
	if p, _ := jobs.ParseRebootParams(j.Params); err != nil || j.Type != jobs.TypeReboot || p.Delay() != 15*time.Minute {
		t.Fatalf("job: %+v %v", j, err)
	}
	if l := pede(maq+"/reiniciar", url.Values{"aviso": {"5"}, "quando": {"agora"}}); l != maq+"?erro=reinicio-aberto&job=1" {
		t.Errorf("segundo: %s", l)
	}
	// Windows com reinício pendente (o WSUS instalou e deixou o reinício): também (Aula 8.7).
	win := "92fc3b93-0003"
	_, _ = st.SaveInventory(ctx, win, protocol.InventoryReport{SchemaVersion: 1, Hash: "w", OS: protocol.OSInfo{Family: "windows"}})
	_ = st.SaveScan(ctx, win, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, Reboot: &protocol.RebootStatus{Pending: &sim}})
	if b := pagina(h, "GET", "/painel/maquinas/"+win, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "/reiniciar") || !strings.Contains(b, "shutdown /a") {
		t.Errorf("windows:\n%s", b)
	}
	// Máquina sem reinício pendente (e sem família conhecida): nada é pedido.
	if l := pede("/painel/maquinas/7aa604f7-0002/reiniciar", url.Values{"aviso": {"5"}, "quando": {"agora"}}); l != "/painel/maquinas/7aa604f7-0002?erro=sem-reinicio" {
		t.Errorf("sem pendência: %s", l)
	}
}

// TestPassarParaHTTPSPeloPainel (Aula 9.2): o canal do último check-in aparece no detalhe;
// com a URL pública e a chave, o admin pede a troca da máquina que ainda usa HTTP.
func TestPassarParaHTTPSPeloPainel(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	ctx := context.Background()
	maq := "/painel/maquinas/60ef97e4-0001"

	// Sem URL pública: o canal aparece, o botão não.
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	_, _ = st.CheckIn(ctx, "60ef97e4-0001", "v", "", false)
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "HTTP (porta de transição)") || strings.Contains(b, "/https\"") {
		t.Errorf("sem URL pública:\n%s", b)
	}
	if l := local(pagina(h, "POST", maq+"/https", sessaoAdmin, url.Values{})); l != maq+"?erro=sem-url" {
		t.Errorf("sem URL pública, nada é criado: %s", l)
	}

	h, st, _ = ambientePaginas(t, api.WithJobKey(priv), api.WithPublicURL("https://patchd.exemplo:8443"))
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "nenhum check-in desde a Aula 9.2") || !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/https"`) {
		t.Errorf("sem check-in ainda, o botão aparece:\n%s", b)
	}
	_, _ = st.CheckIn(ctx, "60ef97e4-0001", "v", "", false)
	if b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String(); strings.Contains(b, "/https\"") {
		t.Error("leitura não pede a troca")
	}
	if l := local(pagina(h, "POST", maq+"/https", sessaoAdmin, url.Values{})); l != maq+"?criado=1&job=1" {
		t.Fatalf("troca: %s", l)
	}
	if l := local(pagina(h, "POST", maq+"/https", sessaoAdmin, url.Values{})); l != maq+"?erro=https-aberto&job=1" {
		t.Errorf("segundo clique: %s", l)
	}
	env, _ := st.DeliverJobs(ctx, "60ef97e4-0001", time.Now().UTC())
	j, err := jobs.Verify(pub, env[0], "60ef97e4-0001", time.Now().UTC(), 0)
	if p, _ := jobs.ParseServerURLParams(j.Params); err != nil || j.Type != jobs.TypeServerURL || p.URL != "https://patchd.exemplo:8443" {
		t.Fatalf("job: %+v %v", j, err)
	}
	// Já por HTTPS: canal HTTPS, sem botão.
	_, _ = st.CheckIn(ctx, "60ef97e4-0001", "v", "", true)
	if b := pagina(h, "GET", maq, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dd>HTTPS</dd>") || strings.Contains(b, "/https\"") {
		t.Errorf("já em HTTPS:\n%s", b)
	}
}
