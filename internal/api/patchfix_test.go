package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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

// Patch › Sistema por correção: as CVEs de um KB viram uma linha; filtros de sistema e
// severidade; marcar KBs e aprovar em lote aprova todas as CVEs deles, depois da conferência.
func TestPatchPorCorrecao(t *testing.T) {
	agora := time.Now().UTC()
	kb1, kb2 := "26300.9711 (KB5127648)", "26300.9800 (KB5130000)"
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{
			{MachineID: "w1", Hostname: "pc-1", OS: "Windows 11", PendingUnit: "CVEs"},
			{MachineID: "w2", Hostname: "pc-2", OS: "Windows 11", PendingUnit: "CVEs"},
			{MachineID: "l1", Hostname: "srv-1", OS: "Ubuntu", PendingUnit: "pacotes"},
		}},
		itens: map[string][]protocol.PendingItem{
			"w1": {{ID: "CVE-2026-1", FixedIn: kb1, Severity: "Critical", Exploited: true}, {ID: "CVE-2026-2", FixedIn: kb1, Severity: "Important"},
				{ID: "CVE-2026-3", FixedIn: kb2, Severity: "Moderate"}},
			"w2": {{ID: "CVE-2026-1", FixedIn: kb1, Severity: "Critical", Exploited: true}},
			"l1": {{ID: "curl", FixedIn: "8.18.0-1ubuntu2.10", Severity: "high", Packages: []string{"curl"}}},
		},
	}
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	_ = st.CreatePanelSession(context.Background(), identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src))

	b := pagina(h, "GET", "/painel/patch?ver=correcao", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Correções pendentes", "<code>KB5127648</code>", "<code>KB5130000</code>", "<code>curl</code>", "1–3 de 3 correções",
		`<td class="num">2 CVE(s)</td>`, `%28KB5127648%29"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("lista: falta %q", quer)
		}
	}
	// KB5127648 afeta 2 máquinas; filtros.
	if !strings.Contains(b, `<td class="num">2</td>`) {
		t.Error("sistemas afetados do KB")
	}
	if b := pagina(h, "GET", "/painel/patch?ver=correcao&sistema=linux", sessaoAdmin, nil).Body.String(); strings.Contains(b, "KB5127648") || !strings.Contains(b, "<code>curl</code>") {
		t.Error("filtro de sistema")
	}
	if b := pagina(h, "GET", "/painel/patch?ver=correcao&severidade=critica", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "KB5127648") || strings.Contains(b, "KB5130000") {
		t.Error("filtro de severidade")
	}
	if b := pagina(h, "GET", "/painel/patch?sistema=windows", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "CVE-2026-3") || strings.Contains(b, ">curl<") {
		t.Error("filtro de sistema na visão por item")
	}

	// Conferência das marcadas: nada aprovado ainda.
	f := url.Values{"ver": {"correcao"}, "sel": {"corr=" + url.QueryEscape(kb1)}}
	r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, f)
	b = r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "1 correção será aprovada: 2 item(ns) aguardando") || !strings.Contains(b, `name="ver" value="correcao"`) {
		t.Fatalf("conferência: %d\n%.3000s", r.Code, b)
	}
	if l, _ := st.PatchApprovals(context.Background()); len(l) != 0 {
		t.Fatal("a conferência aprovou")
	}
	f.Set("confirmar", "1")
	f.Set("nota", "testado no piloto")
	r = pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, f)
	if loc := r.Header().Get("Location"); r.Code != http.StatusSeeOther || !strings.Contains(loc, "lote=2") || !strings.Contains(loc, "ver=correcao") {
		t.Fatalf("aprovar: %d %s", r.Code, loc)
	}
	l, _ := st.PatchApprovals(context.Background())
	if len(l) != 2 || l[0].FixedIn != kb1 || l[1].FixedIn != kb1 {
		t.Fatalf("aprovações: %+v", l)
	}
	b = pagina(h, "GET", "/painel/patch?ver=correcao", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<span class="estado em_dia">aprovada</span>`) {
		t.Error("o KB aparece aprovado")
	}
	// Todas do filtro (conferência): só as que ainda aguardam.
	r = pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"ver": {"correcao"}, "filtro": {"1"}})
	if b := r.Body.String(); !strings.Contains(b, "2 correções serão aprovadas") || strings.Contains(b, "KB5127648") {
		t.Errorf("todas do filtro:\n%.3000s", b)
	}
	// Leitura não aprova.
	if r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoTeste, f); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
}
