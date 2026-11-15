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
	if !strings.Contains(b, `<form class="formulario pergunta" method="post" action="/painel/perguntas">`) || !strings.Contains(b, "Registro (Windows): qual o valor?") ||
		!strings.Contains(b, `<optgroup label="Sobre cada usuário da máquina">`) || !strings.Contains(b, `class="muted dica dica-extensoes"`) ||
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

// As perguntas por usuário (extensões dos navegadores): só vão a quem anuncia a capacidade; a
// resposta é uma lista por máquina, que a tela junta por extensão, com filtro, a página de
// quem tem cada uma e o CSV de uma linha por item.
func TestPerguntaDeExtensoesPorUsuario(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	maqs := []protocol.EnrollResponse{registra(t, h, tok), registra(t, h, tok), registra(t, h, tok)}
	for i, m := range maqs {
		st.SaveInventory(ctx, m.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + m.MachineID, OS: protocol.OSInfo{Hostname: "pc-" + strconv.Itoa(i)}})
	}
	pergunta := func(f url.Values) string {
		t.Helper()
		return local(pagina(h, "POST", "/painel/perguntas", sessaoAdmin, f))
	}
	// Nenhum agente anunciou a capacidade: a pergunta não sai.
	if l := pergunta(url.Values{"sensor": {"extensoes"}, "arg": {""}, "alvo": {"todas"}}); !strings.Contains(l, "erro=agente") {
		t.Fatalf("sem agente capaz: %s", l)
	}
	if b := pagina(h, "GET", "/painel/perguntas?erro=agente", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Atualize os agentes") {
		t.Error("o erro não explica o que fazer")
	}
	// Duas máquinas com agente novo; a terceira (e as do ambiente) ficam de fora.
	for _, m := range maqs[:2] {
		if err := st.SetMachineCapabilities(ctx, m.MachineID, []string{protocol.CapabilityJobs, protocol.CapabilityUserQueries}); err != nil {
			t.Fatal(err)
		}
	}
	if l := pergunta(url.Values{"sensor": {"arquivo-usuario"}, "arg": {`C:\Users\ana\x.exe`}, "alvo": {"todas"}}); !strings.Contains(l, "erro=pergunta") {
		t.Errorf("caminho fora da pasta do usuário: %s", l)
	}
	l := pergunta(url.Values{"sensor": {"extensoes"}, "arg": {""}, "alvo": {"todas"}})
	if l != "/painel/perguntas/1" {
		t.Fatalf("pergunta: %s", l)
	}
	q, _, _ := st.Question(ctx, 1)
	if q.Machines != 2 || q.Skipped != 3 || q.Sensor != jobs.SensorExtensions {
		t.Fatalf("pergunta gravada: %+v", q)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "pergunta.criar"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "Extensões dos navegadores: todas · todas as máquinas · 2 máquina(s) · 3 de fora") {
		t.Errorf("auditoria: %+v", ev)
	}
	b := pagina(h, "GET", "/painel/perguntas/1", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"3 de fora: o agente delas é anterior a esta pergunta", "Ainda sem achados", "aguardando resposta", `http-equiv="refresh"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("antes das respostas: faltou %q", quer)
		}
	}

	responde := func(m protocol.EnrollResponse, res protocol.JobResult) {
		t.Helper()
		env, _ := st.DeliverJobs(ctx, m.MachineID, time.Now())
		for _, e := range env {
			j, err := jobs.Verify(pub, e, m.MachineID, time.Now(), 0)
			if err != nil || j.Type != jobs.TypeQuery {
				t.Fatalf("job: %v %+v", err, j)
			}
			res.FinishedAt = time.Now().UTC()
			corpo, _ := json.Marshal(res)
			if r := envia(h, "POST", protocol.JobResultPath+strconv.FormatInt(j.ID, 10)+"/result", m.Credential, corpo); r.Code >= 300 {
				t.Fatalf("resultado: %d %s", r.Code, r.Body)
			}
		}
	}
	const bloq, solta = "cjpalhdlnbpafiamejdnhcphjbkeiagm", "abcdefghijklmnopabcdefghijklmnop"
	com, _ := json.Marshal(jobs.QueryAnswer{Value: "com extensões", Extra: "3 extensão(ões) de 2 usuário(s)"})
	responde(maqs[0], protocol.JobResult{Status: protocol.JobResultOK, Detail: string(com), Items: []protocol.AnswerItem{
		{Key: bloq, Name: "Bloqueador Origem", Version: "1.60.0", User: "ana", Source: "Chrome", Where: "Default", Note: "acesso a todos os sites"},
		{Key: bloq, Name: "Bloqueador Origem", Version: "1.59.0", User: "bruno", Source: "Edge", Where: "Default", Note: "acesso a todos os sites"},
		{Key: solta, Name: "=Ladra <b>de</b> Senhas", Version: "0.1", User: "bruno", Source: "Chrome", Where: "Profile 1",
			Note: "sem compactação (modo de desenvolvedor); pasta C:\\Users\\bruno\\ext"},
		{User: "sem-chave-nem-nome"},
	}})
	responde(maqs[1], protocol.JobResult{Status: protocol.JobResultOK, Detail: string(com), Items: []protocol.AnswerItem{
		{Key: bloq, Name: "Bloqueador Origem", Version: "1.60.0", User: "carla", Source: "Chrome", Where: "Default"},
		{Key: "uBlock0@raymondhill.net", Name: "uBlock Origin", Version: "1.61.2", User: "carla", Source: "Firefox", Where: "abc.default-release"},
	}})
	b = pagina(h, "GET", "/painel/perguntas/1", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"3 extensão(ões) diferente(s), em 3 usuário(s) de 2 máquina(s)", "Extensões encontradas", ">Bloqueador Origem</a>",
		"1.59.0, 1.60.0", "Chrome, Edge", `/painel/perguntas/1/item?k=` + bloq, "=Ladra &lt;b&gt;de&lt;/b&gt; Senhas", "Máquinas, pela resposta",
		`<span class="nivel-ok">com extensões</span>`, "uma linha por extensão de cada usuário"} {
		if !strings.Contains(b, quer) {
			t.Errorf("tela da pergunta: faltou %q", quer)
		}
	}
	if strings.Contains(b, `http-equiv="refresh"`) || strings.Contains(b, "sem-chave-nem-nome") {
		t.Error("a tela ainda se atualiza depois de todos responderem, ou guardou item sem chave")
	}
	if i, j := strings.Index(b, ">Bloqueador Origem</a>"), strings.Index(b, ">uBlock Origin</a>"); i < 0 || j < i {
		t.Error("a ordem padrão é a das mais comuns primeiro")
	}
	// O filtro acha pelo usuário e pela observação; a ordem das raras inverte.
	b = pagina(h, "GET", "/painel/perguntas/1?q=desenvolvedor", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Ladra") || strings.Contains(b, ">uBlock Origin</a>") || !strings.Contains(b, "1 com “desenvolvedor”") {
		t.Errorf("filtro pela observação:\n%s", b)
	}
	b = pagina(h, "GET", "/painel/perguntas/1?q=CARLA", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">uBlock Origin</a>") || strings.Contains(b, "Ladra") {
		t.Error("filtro pelo usuário")
	}
	if b = pagina(h, "GET", "/painel/perguntas/1?q=100%25", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Nada com “100%”") {
		t.Error("o % do filtro não é curinga")
	}
	b = pagina(h, "GET", "/painel/perguntas/1?ordem=raras", sessaoAdmin, nil).Body.String()
	if i, j := strings.Index(b, ">Bloqueador Origem</a>"), strings.Index(b, ">uBlock Origin</a>"); i < 0 || j < 0 || j > i {
		t.Error("as raras primeiro")
	}
	// Quem tem a extensão.
	r := pagina(h, "GET", "/painel/perguntas/1/item?k="+bloq, sessaoAdmin, nil)
	b = r.Body.String()
	for _, quer := range []string{"<h1>Bloqueador Origem</h1>", "3 usuário(s) em 2 máquina(s)", ">pc-0<", ">pc-1<", "<td>bruno</td>", "<td>Edge</td>",
		"https://chromewebstore.google.com/detail/" + bloq, `rel="noopener noreferrer"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("página do item: faltou %q", quer)
		}
	}
	if b = pagina(h, "GET", "/painel/perguntas/1/item?k=uBlock0%40raymondhill.net", sessaoAdmin, nil).Body.String(); !strings.Contains(b,
		"https://addons.mozilla.org/firefox/addon/uBlock0@raymondhill.net/") || strings.Contains(b, "chromewebstore") {
		t.Error("o ID do Firefox aponta para a loja da Mozilla")
	}
	for _, path := range []string{"/painel/perguntas/1/item?k=nao-existe", "/painel/perguntas/1/item", "/painel/perguntas/9/item?k=x"} {
		if r := pagina(h, "GET", path, sessaoAdmin, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, r.Code)
		}
	}
	if r := pagina(h, "GET", "/painel/perguntas/1/item?k="+bloq, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura vê quem tem a extensão: %d", r.Code)
	}
	// O CSV: uma linha por item, com o nome que começa por "=" neutralizado.
	r = pagina(h, "GET", "/painel/perguntas/1/csv", sessaoAdmin, nil)
	csv := r.Body.String()
	if r.Code != 200 || strings.Count(csv, "\r\n") != 6 || !strings.Contains(csv, "maquina;nome;usuario;item;id;versao;navegador;perfil;observacao;resposta_da_maquina") ||
		!strings.Contains(csv, ";pc-0;bruno;'=Ladra <b>de</b> Senhas;"+solta+";0.1;Chrome;Profile 1;") || !strings.Contains(csv, ";com extensões\r\n") {
		t.Errorf("CSV: %d\n%s", r.Code, csv)
	}
	// O resultado repetido não duplica os itens; e o job de outra máquina não grava nos desta.
	qjobs, _ := st.QuestionJobs(ctx, 1)
	for _, j := range qjobs {
		outra := maqs[0].MachineID
		if j.MachineID == outra {
			outra = maqs[1].MachineID
		}
		if err := st.SaveQuestionItems(ctx, j.JobID, outra, []protocol.AnswerItem{{Key: "x", Name: "x"}}); err != nil {
			t.Fatal(err)
		}
		if j.MachineID == maqs[1].MachineID { // o mesmo resultado, de novo
			if err := st.SaveQuestionItems(ctx, j.JobID, j.MachineID, []protocol.AnswerItem{
				{Key: bloq, Name: "Bloqueador Origem", Version: "1.60.0", User: "carla", Source: "Chrome", Where: "Default"},
				{Key: "uBlock0@raymondhill.net", Name: "uBlock Origin", Version: "1.61.2", User: "carla", Source: "Firefox", Where: "abc.default-release"},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if tot, _ := st.QuestionItemTotals(ctx, 1); tot.Items != 5 || tot.Keys != 3 || tot.Machines != 2 || tot.Users != 3 {
		t.Errorf("totais: %+v", tot)
	}
	// Uma pergunta comum não guarda itens, mesmo que o agente os mande.
	if l := pergunta(url.Values{"sensor": {"processo"}, "arg": {"firefox.exe"}, "alvo": {"todas"}}); l != "/painel/perguntas/2" {
		t.Fatalf("pergunta comum: %s", l)
	}
	rodando, _ := json.Marshal(jobs.QueryAnswer{Value: "rodando"})
	responde(maqs[0], protocol.JobResult{Status: protocol.JobResultOK, Detail: string(rodando), Items: []protocol.AnswerItem{{Key: "k", Name: "intruso"}}})
	if tot, _ := st.QuestionItemTotals(ctx, 2); tot.Items != 0 {
		t.Errorf("pergunta comum guardou itens: %+v", tot)
	}
	if r := pagina(h, "GET", "/painel/perguntas/2/item?k=k", sessaoAdmin, nil); r.Code != http.StatusNotFound {
		t.Errorf("item de pergunta comum: %d", r.Code)
	}
}
