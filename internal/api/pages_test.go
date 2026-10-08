package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

const sessaoAdmin = panel.SessionPrefix + "sessao-admin"

// ambientePaginas: a frota falsa com três máquinas (uma com nome hostil), duas sessões
// (leitura e admin) e uma máquina real no armazenamento para aposentar.
func ambientePaginas(t *testing.T) (http.Handler, *store.Memory, *syncBuffer) {
	t.Helper()
	agora := time.Now().UTC()
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	if err := st.CreatePanelSession(context.Background(), identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		if _, err := st.SaveInventory(context.Background(), id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, OS: protocol.OSInfo{Hostname: "vm"}}); err != nil {
			t.Fatal(err)
		}
	}
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora,
			Counts: map[string]int{"faltando": 1, "reboot_pendente": 0, "sem_dado_recente": 0, "desconhecido": 1, "em_dia": 1},
			Machines: []protocol.ComplianceStatus{
				{MachineID: "60ef97e4-0001", Hostname: "vm-ubuntu", OS: "Ubuntu 26.04", State: "faltando", Pending: 1, PendingUnit: "pacotes", Exploited: 1,
					Reasons: []string{"1 pacote com correção pendente (1 com exploração conhecida)"}, LastSeenAt: agora.Add(-2 * time.Hour), EvaluatedAt: agora},
				{MachineID: "92fc3b93-0003", Hostname: `<script>alert(1)</script>`, OS: "Windows 11 Pro 26H2", State: "desconhecido",
					Reasons: []string{"sem avaliação: 26H2 fora do catálogo"}, LastSeenAt: agora.Add(-5 * time.Minute), EvaluatedAt: agora},
				{MachineID: "aaaa0000-0004", State: "em_dia", Reasons: []string{}, LastSeenAt: agora, EvaluatedAt: agora},
			}},
		itens: map[string][]protocol.PendingItem{"60ef97e4-0001": {
			{ID: "curl", Severity: "high", Exploited: true, Installed: "8.18.0-1ubuntu2.7", FixedIn: "8.18.0-1ubuntu2.10", Note: "USN-9002-1"}}},
	}
	log := &syncBuffer{}
	return api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithCompliance(src)), st, log
}

func pagina(h http.Handler, method, path, cookie string, form url.Values, cabecalhos ...string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
	}
	for i := 0; i+1 < len(cabecalhos); i += 2 {
		req.Header.Set(cabecalhos[i], cabecalhos[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPaginaDaFrota(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	r := pagina(h, "GET", "/painel/", sessaoTeste, nil)
	b := r.Body.String()
	for _, tr := range []string{
		`<a href="/painel/maquinas/60ef97e4-0001"><code>60ef97e4</code></a>`,
		"1 pacote", `<span class="expl">1</span>`, "há 2 h", "há 5 min",
		"1 pacote com correção pendente", `<b>3</b>todas`,
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"consulta (local) · leitura",
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	if strings.Contains(b, "<script>") {
		t.Error("o nome da máquina tem de sair escapado")
	}
	// O que pede ação vem primeiro: a ordem é a da avaliação.
	if strings.Index(b, "60ef97e4") > strings.Index(b, "92fc3b93") {
		t.Error("ordem")
	}

	// Filtro: só as do estado; filtro desconhecido é ignorado.
	b = pagina(h, "GET", "/painel/?estado=faltando", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "60ef97e4") || strings.Contains(b, "92fc3b93") || !strings.Contains(b, `href="/painel/?estado=faltando" class="ativo"`) {
		t.Errorf("filtro:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/?estado=%3Cx%3E", sessaoTeste, nil).Body.String(); !strings.Contains(b, "92fc3b93") || strings.Contains(b, "<x>") {
		t.Error("filtro inválido: mostra tudo e não reflete o valor")
	}
	if r := pagina(h, "GET", "/painel/", "", nil); r.Code != http.StatusSeeOther {
		t.Errorf("sem sessão: %d", r.Code)
	}
}

func TestPaginaDaMaquina(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	r := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoTeste, nil)
	b := r.Body.String()
	for _, tr := range []string{"vm-ubuntu", `estado faltando`, "<code>curl</code>", "8.18.0-1ubuntu2.10", "USN-9002-1", `<span class="expl">sim</span>`} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	if strings.Contains(b, "Aposentar") {
		t.Error("o perfil leitura não vê o botão de aposentar")
	}
	if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/aposentar"`) {
		t.Error("o admin vê o formulário de aposentar")
	}
	for _, p := range []string{"/painel/maquinas/nao-existe", "/painel/maquinas/..%2F..%2Fetc"} {
		if r := pagina(h, "GET", p, sessaoTeste, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
}

func TestAposentarERestaurarPeloPainel(t *testing.T) {
	h, st, log := ambientePaginas(t)
	caminho := "/painel/maquinas/7aa604f7-0002/aposentar"
	motivo := url.Values{"motivo": {"registro antigo da VM reinstalada"}}

	// Leitura: 403, e nada muda.
	if r := pagina(h, "POST", caminho, sessaoTeste, motivo); r.Code != http.StatusForbidden || !strings.Contains(r.Body.String(), "Sem permissão") {
		t.Errorf("leitura: %d", r.Code)
	}
	if !strings.Contains(log.String(), "ação de admin recusada") {
		t.Error("a tentativa do perfil leitura vai para o log")
	}
	// Outra origem: 403 antes de tudo.
	if r := pagina(h, "POST", caminho, sessaoAdmin, motivo, "Sec-Fetch-Site", "cross-site"); r.Code != http.StatusForbidden {
		t.Errorf("outra origem: %d", r.Code)
	}
	// Sem motivo: volta ao detalhe com o erro.
	if r := pagina(h, "POST", caminho, sessaoAdmin, url.Values{"motivo": {" x "}}); r.Code != http.StatusSeeOther || !strings.Contains(r.Header().Get("Location"), "erro=motivo") {
		t.Errorf("sem motivo: %d %s", r.Code, r.Header().Get("Location"))
	}
	if ap, _ := st.RetiredMachines(context.Background()); len(ap) != 0 {
		t.Fatal("nenhuma das tentativas acima pode aposentar")
	}

	r := pagina(h, "POST", caminho, sessaoAdmin, motivo)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/aposentadas?aposentada=1" {
		t.Fatalf("aposentar: %d %s", r.Code, r.Header().Get("Location"))
	}
	ap, _ := st.RetiredMachines(context.Background())
	if len(ap) != 1 || ap[0].RetiredBy != "thyago" || ap[0].Reason != "registro antigo da VM reinstalada" {
		t.Fatalf("aposentada: %+v", ap)
	}
	if !strings.Contains(log.String(), "máquina aposentada pelo painel") {
		t.Error("a aposentadoria vai para o log")
	}
	b := pagina(h, "GET", "/painel/aposentadas?aposentada=1", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "registro antigo da VM reinstalada") || !strings.Contains(b, "thyago") || !strings.Contains(b, "Máquina aposentada") ||
		strings.Contains(b, "Restaurar") {
		t.Errorf("aposentadas (leitura):\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/aposentadas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/maquinas/7aa604f7-0002/restaurar"`) {
		t.Error("o admin vê o botão de restaurar")
	}

	r = pagina(h, "POST", "/painel/maquinas/7aa604f7-0002/restaurar", sessaoAdmin, url.Values{})
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/maquinas/7aa604f7-0002?restaurada=1" {
		t.Errorf("restaurar: %d %s", r.Code, r.Header().Get("Location"))
	}
	if ap, _ := st.RetiredMachines(context.Background()); len(ap) != 0 {
		t.Error("restaurada")
	}
	if !bytes.Contains([]byte(log.String()), []byte("máquina restaurada pelo painel")) {
		t.Error("a restauração vai para o log")
	}
}
