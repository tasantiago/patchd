package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/canal"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Perguntas ao parque pelo painel: a pergunta vira um job por máquina do alvo; as respostas
// (o detalhe do resultado) aparecem agrupadas, com a espera e o CSV. Só admin.
func TestPerguntasAoParquePeloPainel(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := jobs.GenerateKey()
	hub := canal.New()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithChannel(hub))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	maqs := []protocol.EnrollResponse{registra(t, h, tok), registra(t, h, tok), registra(t, h, tok)}
	for i, m := range maqs {
		st.SaveInventory(ctx, m.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + m.MachineID, OS: protocol.OSInfo{Hostname: "pc-" + strconv.Itoa(i)}})
	}
	st.SetMachineRing(ctx, maqs[0].MachineID, 2)

	b := pagina(h, "GET", "/painel/perguntas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<form class="grade" method="post" action="/painel/perguntas">`) || !strings.Contains(b, "Registro (Windows): qual o valor?") ||
		!strings.Contains(b, "máquina(s) no canal em tempo real agora") {
		t.Fatalf("tela:\n%s", b)
	}
	if r := pagina(h, "GET", "/painel/perguntas", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura vê as perguntas: %d", r.Code)
	}
	pergunta := func(f url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", "/painel/perguntas", sessaoAdmin, f))
	}
	if l := pergunta(url.Values{"sensor": {"registro"}, "arg": {`HKLM\SAM\SAM`}, "alvo": {"todas"}}); !strings.Contains(l, "erro=pergunta") || !strings.Contains(l, "SAM") {
		t.Errorf("SAM: %s", l)
	}
	if l := pergunta(url.Values{"sensor": {"arquivo"}, "arg": {`C:\x.dll`}, "alvo": {"7"}}); !strings.Contains(l, "erro=alvo") {
		t.Errorf("grupo vazio: %s", l)
	}
	// Grupo 2: só uma máquina.
	if l := pergunta(url.Values{"sensor": {"processo"}, "arg": {"firefox.exe"}, "alvo": {"2"}}); l != "/painel/perguntas/1" {
		t.Fatalf("grupo: %s", l)
	}
	if q, _, _ := st.Question(ctx, 1); q.Machines != 1 || q.Target != "grupo de implantação 2" {
		t.Errorf("pergunta 1: %+v", q)
	}
	// Todas: três jobs, que acordam o canal (o aviso fica guardado para a próxima espera).
	l := pergunta(url.Values{"sensor": {"arquivo"}, "arg": {`%ProgramFiles%\App\app.dll`}, "alvo": {"todas"}})
	if l != "/painel/perguntas/2" {
		t.Fatalf("todas: %s", l)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "pergunta.criar"})
	if len(ev) != 2 || !strings.Contains(ev[0].Detail, `Arquivo: %ProgramFiles%\App\app.dll · todas as máquinas · 5 máquina(s)`) {
		t.Errorf("auditoria: %+v", ev)
	}
	b = pagina(h, "GET", "/painel/perguntas/2", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "aguardando resposta") || !strings.Contains(b, `http-equiv="refresh"`) || !strings.Contains(b, "0 de 5") {
		t.Fatalf("antes das respostas:\n%s", b)
	}

	// As máquinas respondem: duas com a versão, uma com agente antigo (recusa).
	responde := func(m protocol.EnrollResponse, status, detail string) {
		t.Helper()
		env, _ := st.DeliverJobs(ctx, m.MachineID, time.Now())
		for _, e := range env {
			j, err := jobs.Verify(pub, e, m.MachineID, time.Now(), 0)
			if err != nil || j.Type != jobs.TypeQuery {
				continue
			}
			p, _ := jobs.ParseQueryParams(j.Params)
			if p.Sensor != jobs.SensorFile {
				continue
			}
			corpo, _ := json.Marshal(protocol.JobResult{Status: status, Detail: detail, FinishedAt: time.Now().UTC()})
			if r := envia(h, "POST", protocol.JobResultPath+strconv.FormatInt(j.ID, 10)+"/result", m.Credential, corpo); r.Code >= 300 {
				t.Fatalf("resultado: %d %s", r.Code, r.Body)
			}
		}
	}
	versao, _ := json.Marshal(jobs.QueryAnswer{Value: "2.1.3", Extra: "1234 bytes; sha256 ab"})
	responde(maqs[0], protocol.JobResultOK, string(versao))
	responde(maqs[1], protocol.JobResultOK, string(versao))
	responde(maqs[2], protocol.JobResultRejected, `tipo de job desconhecido "pergunta"`)
	b = pagina(h, "GET", "/painel/perguntas/2", sessaoAdmin, nil).Body.String()
	// As 2 máquinas do ambiente de teste (sem agente aqui) continuam aguardando.
	for _, quer := range []string{"3 de 5", "2 aguardando", `<span class="nivel-ok">2.1.3</span>`, "2 máquina(s) · 40%", "agente sem as perguntas (atualize o agente)",
		"1234 bytes; sha256 ab", ">pc-0<"} {
		if !strings.Contains(b, quer) {
			t.Errorf("respostas: faltou %q", quer)
		}
	}
	r := pagina(h, "GET", "/painel/perguntas/2/csv", sessaoAdmin, nil)
	if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") || strings.Count(r.Body.String(), "2.1.3") != 2 {
		t.Errorf("CSV: %d\n%s", r.Code, r.Body)
	}
	if b := pagina(h, "GET", "/painel/perguntas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "3 de 5") || !strings.Contains(b, "0 de 1") {
		t.Errorf("lista:\n%s", b)
	}
}
