package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestInicioDashboardECompatibilidade(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	r := pagina(h, "GET", "/painel/", sessaoTeste, nil)
	b := r.Body.String()
	if r.Code != http.StatusOK {
		t.Fatalf("início: %d", r.Code)
	}
	for _, want := range []string{"Visão geral do ambiente", "Conformidade da frota", "Composição do ambiente", "Execuções · últimos 7 dias", "Equipamentos para priorizar",
		`href="/painel/" title="Início" class="ativo"`, `href="/painel/frota"`, `href="/painel/inventario?status=offline"`, "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(b, want) {
			t.Errorf("faltou %q", want)
		}
	}
	for _, forbidden := range []string{"<script", "style=", `href="/painel/configuracao`, `action="/painel/aneis/maquinas"`} {
		if strings.Contains(b, forbidden) {
			t.Errorf("início de leitura contém %q", forbidden)
		}
	}
	if r.Header().Get("Cache-Control") != "no-store" || strings.Contains(r.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Error("dashboard deve manter a política de cache e CSP do painel")
	}
	legacy := pagina(h, "GET", "/painel/?estado=faltando", sessaoTeste, nil).Body.String()
	fleet := pagina(h, "GET", "/painel/frota?estado=faltando", sessaoTeste, nil).Body.String()
	if legacy != fleet || !strings.Contains(fleet, "<h1>Frota</h1>") {
		t.Error("links antigos filtrados e rota de frota devem abrir a mesma lista")
	}
	for _, path := range []string{"/painel/", "/painel/frota"} {
		if r := pagina(h, "GET", path, "", nil); r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/entrar" {
			t.Errorf("%s sem sessão: %d", path, r.Code)
		}
	}
}

// Override only the aggregates used by the dashboard; sessions and navigation use Memory.
type dashboardTestStore struct {
	*store.Memory
	machines []protocol.MachineSummary
	counts []store.JobCount
	failInventory, failJobs bool
	countSince, finishedSince time.Time
	finishedStates []string
}

func (s *dashboardTestStore) Machines(context.Context) ([]protocol.MachineSummary, error) {
	if s.failInventory {
		return nil, errors.New("detalhe interno que não pode aparecer no HTML")
	}
	return s.machines, nil
}

func (s *dashboardTestStore) JobCounts(_ context.Context, since time.Time) ([]store.JobCount, error) {
	s.countSince = since
	if s.failJobs {
		return nil, errors.New("consulta indisponível")
	}
	return s.counts, nil
}

func (s *dashboardTestStore) JobsFinishedSince(_ context.Context, states []string, since time.Time) (int, error) {
	s.finishedSince, s.finishedStates = since, slices.Clone(states)
	return 3, nil
}

func dashboardHandler(t *testing.T, st *dashboardTestStore, src *complianceFalso) http.Handler {
	t.Helper()
	abreSessao(t, st.Memory, sessaoTeste, time.Now().UTC())
	return api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src))
}

func dashboardCard(t *testing.T, html, label string) string {
	t.Helper()
	i := strings.Index(html, `<span class="kpi-rot">`+label+`</span>`)
	if i < 0 {
		t.Fatalf("cartão ausente: %s", label)
	}
	end := strings.Index(html[i:], "</a>")
	if end < 0 {
		t.Fatal("cartão sem fechamento")
	}
	return html[i:i+end]
}

func TestDashboardContagensELimitesTemporais(t *testing.T) {
	now := time.Now().UTC()
	st := &dashboardTestStore{Memory: store.NewMemory(), machines: []protocol.MachineSummary{
		{ID: "atual", OSName: "Windows 11", LastSeenAt: now},
		{ID: "antiga", OSName: "Ubuntu 24.04", LastSeenAt: now.Add(-3*time.Hour)},
		{ID: "sem-contato"},
	}, counts: []store.JobCount{{State: jobs.StateDone, N: 7}, {State: jobs.StateFailed, N: 1}, {State: jobs.StateDelivered, N: 2}}}
	src := &complianceFalso{fleet: protocol.ComplianceFleet{EvaluatedAt: now, Machines: []protocol.ComplianceStatus{
		{MachineID: "atual", State: "em_dia", LastSeenAt: now},
		{MachineID: "antiga", State: "sem_dado_recente", LastSeenAt: now.Add(-8*24*time.Hour), Exploited: 4},
		{MachineID: "sem-contato", State: "desconhecido"},
	}}}
	h := dashboardHandler(t, st, src)
	b := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String()
	for label, value := range map[string]string{"Equipamentos ativos": "3", "Sem contato recente": "2", "Em dia": "1", "Com exploração conhecida": "1", "Jobs sem sucesso · 24 h": "3"} {
		card := dashboardCard(t, b, label)
		if !strings.Contains(card, ">"+value+"</span>") {
			t.Errorf("%s: %s", label, card)
		}
	}
	if !strings.Contains(dashboardCard(t, b, "Em dia"), "33% de 3 equipamentos avaliados") || !strings.Contains(b, "10</strong> jobs criados no período") {
		t.Error("denominador da conformidade ou total de jobs incorreto")
	}
	if d := st.countSince.Sub(now.Add(-7*24*time.Hour)); d < 0 || d > time.Second {
		t.Errorf("janela dos jobs criados: %v", d)
	}
	if d := st.finishedSince.Sub(now.Add(-24*time.Hour)); d < 0 || d > time.Second {
		t.Errorf("janela dos jobs terminados: %v", d)
	}
	if !slices.Equal(st.finishedStates, []string{jobs.StateFailed, jobs.StateRejected, jobs.StateExpired}) {
		t.Errorf("estados de falha: %v", st.finishedStates)
	}
}

func TestDashboardFalhasParciaisEVazio(t *testing.T) {
	st := &dashboardTestStore{Memory: store.NewMemory(), failInventory: true, failJobs: true}
	h := dashboardHandler(t, st, &complianceFalso{err: errors.New("catálogo fora")})
	r := pagina(h, "GET", "/painel/", sessaoTeste, nil)
	b := r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Conformidade: dados indisponíveis") || !strings.Contains(b, "Dados de execução indisponíveis") {
		t.Fatalf("falha parcial: %d %s", r.Code, b)
	}
	for _, label := range []string{"Equipamentos ativos", "Em dia", "Com exploração conhecida"} {
		if !strings.Contains(dashboardCard(t, b, label), ">—</span>") {
			t.Errorf("%s não pode assumir zero durante falha", label)
		}
	}
	if strings.Contains(b, "detalhe interno") || strings.Contains(b, "catálogo fora") || !strings.Contains(dashboardCard(t, b, "Jobs sem sucesso · 24 h"), ">3</span>") {
		t.Error("a falha deve preservar outras fontes e ocultar detalhes internos")
	}
	empty := &dashboardTestStore{Memory: store.NewMemory()}
	b = pagina(dashboardHandler(t, empty, &complianceFalso{fleet: protocol.ComplianceFleet{EvaluatedAt: time.Now().UTC()}}), "GET", "/painel/", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "Nenhum equipamento ativo cadastrado") || strings.Contains(b, "100%") || !strings.Contains(dashboardCard(t, b, "Em dia"), ">—</span>") {
		t.Error("frota vazia não representa conformidade de 100%")
	}
}

func TestDashboardPriorizaExploradasEExplicitaCatalogo(t *testing.T) {
	now := time.Now().UTC()
	src := &complianceFalso{fleet: protocol.ComplianceFleet{EvaluatedAt: now}}
	for i := 0; i < 12; i++ {
		src.fleet.Machines = append(src.fleet.Machines, protocol.ComplianceStatus{MachineID: string(rune('a'+i)), Hostname: "com-pendencia", State: "faltando", LastSeenAt: now})
	}
	src.fleet.Machines = append(src.fleet.Machines, protocol.ComplianceStatus{MachineID: "explorada", Hostname: "<img src=x onerror=alert(1)>", State: "sem_dado_recente", Exploited: 1,
		LastSeenAt: now.Add(-8*24*time.Hour), CatalogWarnings: []string{"fonte atrasada"}})
	st := &dashboardTestStore{Memory: store.NewMemory()}
	b := pagina(dashboardHandler(t, st, src), "GET", "/painel/", sessaoTeste, nil).Body.String()
	i := strings.Index(b, `aria-labelledby="dash-prioridades"`)
	if i < 0 {
		t.Fatal("sem prioridades")
	}
	priorities := b[i:]
	if strings.Count(priorities, "<tr><td>") != 8 || !strings.Contains(priorities, "Mais 5 equipamentos") || strings.Index(priorities, "/painel/maquinas/explorada") > strings.Index(priorities, "/painel/maquinas/a") {
		t.Error("prioridades devem ser limitadas e colocar exploração conhecida primeiro")
	}
	if strings.Contains(b, "<img src=x") || !strings.Contains(b, "&lt;img src=x onerror=alert(1)&gt;") || !strings.Contains(b, "Catálogos: há fontes atrasadas") {
		t.Error("escapamento e indicação de catálogo atrasado")
	}
}

func TestDashboardPersonalizacaoPersistenciaEIsolamento(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	r := pagina(h, "GET", "/painel/?personalizar=1&bloco=comunicacao&bloco=fora-da-lista", sessaoTeste, nil)
	var preference *http.Cookie
	for _, cookie := range r.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, "patchd_dashboard_") {
			preference = cookie
		}
	}
	if preference == nil || preference.Value != "comunicacao" || !preference.HttpOnly || preference.Path != "/painel/" || preference.SameSite != http.SameSiteStrictMode || preference.MaxAge <= 0 {
		t.Fatalf("preferência: %v", preference)
	}
	load := func(path, session string, preference *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: session})
		req.AddCookie(preference)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	b := load("/painel/", sessaoTeste, preference).Body.String()
	if strings.Count(b, `aria-labelledby="dash-`) != 1 || !strings.Contains(b, `aria-labelledby="dash-comunicacao"`) || strings.Contains(b, `aria-label="Indicadores para decisão"`) {
		t.Error("a seleção deve persistir e ocultar os demais blocos")
	}
	b = load("/painel/", sessaoAdmin, preference).Body.String()
	if !strings.Contains(b, `aria-labelledby="dash-conformidade"`) || !strings.Contains(b, `aria-labelledby="dash-execucao"`) {
		t.Error("preferências de uma conta não podem alterar a seleção de outra")
	}
	b = load("/painel/?personalizar=restaurar", sessaoTeste, preference).Body.String()
	if strings.Count(b, `aria-labelledby="dash-`) != 8 || !strings.Contains(b, `aria-label="Indicadores para decisão"`) {
		t.Error("restaurar deve reativar todos os nove blocos")
	}
	invalid := *preference
	invalid.Value = "fora-da-lista"
	if b := load("/painel/", sessaoTeste, &invalid).Body.String(); strings.Count(b, `aria-labelledby="dash-`) != 8 {
		t.Error("cookie inválido deve usar a visão completa")
	}
}

func TestDashboardTodosOcultosNaoConsultaFontes(t *testing.T) {
	st := &dashboardTestStore{Memory: store.NewMemory(), failInventory: true, failJobs: true}
	h := dashboardHandler(t, st, &complianceFalso{err: errors.New("não deve consultar")})
	r := pagina(h, "GET", "/painel/?personalizar=1", sessaoTeste, nil)
	b := r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Todos os gráficos estão ocultos") || !strings.Contains(b, "Personalizar gráficos") || strings.Contains(b, "dados indisponíveis") || strings.Contains(b, `aria-labelledby="dash-`) {
		t.Errorf("visão sem blocos deve permitir reconfiguração sem consultas: %d", r.Code)
	}
	if !st.countSince.IsZero() || !st.finishedSince.IsZero() {
		t.Error("blocos desabilitados não devem consultar agregados de jobs")
	}
	for _, cookie := range r.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, "patchd_dashboard_") && cookie.Value != "none" {
			t.Error("a opção de ocultar todos deve ser persistida explicitamente")
		}
	}
}
