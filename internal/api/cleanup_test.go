package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Limpeza de temporários pelos alertas de disco: o botão só nos alertas de disco abertos, a
// conferência com os itens (os opcionais desmarcados) e um job "limpeza" por máquina.
func TestLimpezaPelosAlertas(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	agora := time.Now().UTC().Truncate(time.Minute)
	var amostras []protocol.PerfSample
	for i := 0; i < 12; i++ { // CPU e disco além do limite, dois volumes
		amostras = append(amostras, protocol.PerfSample{At: agora.Add(time.Duration(i-12) * time.Minute), CPU: 97,
			Disks: map[string]float64{"C:": 4, "D:": 3}})
	}
	corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), Perf: amostras})
	if r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo); r.Code != http.StatusOK {
		t.Fatalf("check-in: %d", r.Code)
	}
	l, _ := st.PerfAlerts(ctx, store.PerfAlertFilter{OpenOnly: true})
	var cpu, disco []int64
	for _, a := range l {
		if a.Metric == "disk" {
			disco = append(disco, a.ID)
		} else {
			cpu = append(cpu, a.ID)
		}
	}
	if len(disco) != 2 || len(cpu) != 1 {
		t.Fatalf("alertas: %+v", l)
	}

	b := pagina(h, "GET", "/painel/alertas", sessaoAdmin, nil).Body.String()
	if n := strings.Count(b, `<input type="hidden" name="acao" value="limpar">`); n != 2 {
		t.Errorf("o botão da linha só nos alertas de disco: %d", n)
	}
	if !strings.Contains(b, `name="acao" value="limpar" title="só os alertas de disco">Limpar temporários</button>`) {
		t.Error("sem o botão no lote")
	}
	if b := pagina(h, "GET", "/painel/alertas", sessaoTeste, nil).Body.String(); strings.Contains(b, "Limpar temporários") {
		t.Error("leitura vê a limpeza")
	}
	lote := func(cookie string, f url.Values) *httpResp {
		t.Helper()
		r := pagina(h, "POST", "/painel/alertas/lote", cookie, f)
		return &httpResp{r.Code, local(r), r.Body.String()}
	}
	if r := lote(sessaoTeste, url.Values{"acao": {"limpar"}, "quais": {"todos"}}); r.code != http.StatusForbidden {
		t.Fatalf("leitura limpa: %d", r.code)
	}
	// Só o alerta de CPU marcado: nada a limpar.
	if r := lote(sessaoAdmin, url.Values{"acao": {"limpar"}, "quais": {"marcados"}, "alerta": {itoa(cpu[0])}}); !strings.Contains(r.loc, "erro=lote-vazio") {
		t.Errorf("CPU marcada: %s", r.loc)
	}

	// Todos: a máquina uma vez, com os dois volumes; os opcionais desmarcados.
	f := url.Values{"acao": {"limpar"}, "quais": {"todos"}}
	r := lote(sessaoAdmin, f)
	for _, quer := range []string{"Limpar temporários: 1 máquina(s)", "C:, D:",
		`value="temp_sistema" checked`, `value="temp_usuarios" checked`, `value="patchd" checked`,
		`value="cache_atualizacoes" >`, `value="componentes" >`, "mais de 2 dias"} {
		if !strings.Contains(r.body, quer) {
			t.Errorf("conferência: faltou %q\n%s", quer, r.body)
		}
	}
	if j, _ := st.Jobs(ctx, m.MachineID, 10); len(j) != 0 {
		t.Fatal("a conferência criou job")
	}
	f.Set("lista", regexp.MustCompile(`name="lista" value="([0-9a-f]+)"`).FindStringSubmatch(r.body)[1])
	f.Set("confirmar", "1")
	// Nenhum item: volta com o erro.
	if r := lote(sessaoAdmin, f); r.code != http.StatusOK || !strings.Contains(r.body, "Marque ao menos um item") {
		t.Fatalf("sem itens: %d %s", r.code, r.loc)
	}
	f["item"] = []string{"temp_sistema", "componentes"}
	if r := lote(sessaoAdmin, f); r.loc != "/painel/alertas?limpezas=1" {
		t.Fatalf("limpar: %d %s\n%s", r.code, r.loc, r.body)
	}
	jl, _ := st.Jobs(ctx, m.MachineID, 10)
	if len(jl) != 1 || jl[0].Type != jobs.TypeCleanup || jl[0].CreatedBy != "thyago" {
		t.Fatalf("jobs: %+v", jl)
	}
	j, _, _ := st.JobForMachine(ctx, m.MachineID, jl[0].ID)
	var env jobs.Job
	_ = json.Unmarshal([]byte(j.Payload), &env)
	if p, err := jobs.ParseCleanupParams(env.Params); err != nil || strings.Join(p.Items, ",") != "temp_sistema,componentes" {
		t.Errorf("parâmetros: %+v %v", p, err)
	}
	if b := pagina(h, "GET", "/painel/alertas?limpezas=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "1 job(s) de limpeza criados") {
		t.Error("sem o aviso")
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "limpeza.criar"}); len(ev) != 1 ||
		!strings.Contains(ev[0].Detail, "itens: temporários do sistema, limpeza de componentes (DISM)") {
		t.Errorf("trilha: %+v", ev)
	}
}

type httpResp struct {
	code      int
	loc, body string
}
