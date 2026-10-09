package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
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
