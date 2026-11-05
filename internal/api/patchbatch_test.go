package api_test

import (
	"context"
	"fmt"
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

// ambienteLote: uma máquina Linux com três pacotes pendentes, uma Windows com `extras`
// CVEs (mais as duas fixas), e as sessões de leitura e admin.
func ambienteLote(t *testing.T, extras int) (http.Handler, *store.Memory, *syncBuffer) {
	t.Helper()
	agora := time.Now().UTC()
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	if err := st.CreatePanelSession(context.Background(), identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
		t.Fatal(err)
	}
	cves := []protocol.PendingItem{
		{ID: "CVE-2026-1001", Severity: "Critical", Exploited: true, FixedIn: "26200.7019 (KB5070773)"},
		{ID: "CVE-2026-1002", Severity: "Important", FixedIn: "26200.7019 (KB5070773)"},
	}
	for i := range extras {
		cves = append(cves, protocol.PendingItem{ID: fmt.Sprintf("CVE-2026-%d", 5000+i), Severity: "Moderate", FixedIn: "26200.7019 (KB5070773)"})
	}
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{
			{MachineID: "60ef97e4-0001", Hostname: "vm-ubuntu", State: "faltando", PendingUnit: "pacotes", EvaluatedAt: agora},
			{MachineID: "92fc3b93-0003", Hostname: "pc-win", State: "faltando", PendingUnit: "CVEs", EvaluatedAt: agora},
		}},
		itens: map[string][]protocol.PendingItem{
			"60ef97e4-0001": {
				{ID: "curl", Severity: "high", Exploited: true, FixedIn: "8.18.0-1ubuntu2.10", Packages: []string{"curl"}},
				{ID: "bluez", Severity: "medium", FixedIn: "5.80-1ubuntu0.2", Packages: []string{"bluez"}},
				{ID: "rsync", Severity: "medium", FixedIn: "3.4.1+ds1-7ubuntu0.3", Packages: []string{"rsync"}},
			},
			"92fc3b93-0003": cves,
		},
	}
	log := &syncBuffer{}
	return api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithCompliance(src)), st, log
}

func sel(id, fix string) string { return url.Values{"id": {id}, "fix": {fix}}.Encode() }

func TestAprovacaoEmLote(t *testing.T) {
	h, st, log := ambienteLote(t, 0)
	curl, bluez := sel("curl", "8.18.0-1ubuntu2.10"), sel("bluez", "5.80-1ubuntu0.2")
	aprovados := func() map[string]store.PatchApproval {
		t.Helper()
		l, _ := st.PatchApprovals(t.Context())
		m := map[string]store.PatchApproval{}
		for _, a := range l {
			m[a.Item] = a
		}
		return m
	}

	// Leitura: sem caixas de marcar, e a ação é recusada.
	if b := pagina(h, "GET", "/painel/patch", sessaoTeste, nil).Body.String(); strings.Contains(b, `name="sel"`) || strings.Contains(b, "aprovar-lote") {
		t.Errorf("leitura vê o lote:\n%s", b)
	}
	if r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoTeste, url.Values{"sel": {curl}, "confirmar": {"1"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura aprovando em lote: %d", r.Code)
	}

	// Admin: uma caixa por item aguardando, e o botão do filtro com a contagem.
	b := pagina(h, "GET", "/painel/patch", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`<form method="post" action="/painel/patch/aprovar-lote">`,
		`<input type="checkbox" name="sel" value="fix=8.18.0-1ubuntu2.10&amp;id=curl" aria-label="marcar curl">`,
		`name="filtro" value="1">Conferir todos os 5 aguardando</button>`} {
		if !strings.Contains(b, tr) {
			t.Errorf("lista do admin: faltou %q", tr)
		}
	}

	// 1ª etapa: a conferência mostra os marcados e não grava nada.
	r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"sel": {curl, bluez}})
	b = r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Aprovar 2 itens") || !strings.Contains(b, `<input type="hidden" name="sel" value="fix=5.80-1ubuntu0.2&amp;id=bluez">`) ||
		!strings.Contains(b, "1 com exploração conhecida") || strings.Contains(b, "rsync") {
		t.Fatalf("conferência: %d\n%s", r.Code, b)
	}
	if len(aprovados()) != 0 {
		t.Fatal("a conferência gravou")
	}

	// Observação longa: a conferência volta com o erro, e os itens continuam lá.
	b = pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"sel": {curl, bluez}, "confirmar": {"1"},
		"nota": {strings.Repeat("x", 501)}}).Body.String()
	if !strings.Contains(b, "no máximo 500 caracteres") || !strings.Contains(b, "Aprovar 2 itens") || len(aprovados()) != 0 {
		t.Errorf("nota longa:\n%s", b)
	}

	// 2ª etapa: aprova os dois, com a observação, e volta ao filtro de onde saiu.
	l := local(pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"sel": {curl, bluez}, "confirmar": {"1"},
		"nota": {"piloto ok"}, "status": {"aguardando"}}))
	if l != "/painel/patch?fora=0&ja=0&lote=2&status=aguardando" {
		t.Fatalf("depois do lote: %s", l)
	}
	ap := aprovados()
	if len(ap) != 2 || ap["curl"].ApprovedBy != "thyago" || ap["bluez"].Note != "piloto ok" {
		t.Fatalf("gravado: %+v", ap)
	}
	if !strings.Contains(log.String(), `msg="patch aprovado" item=bluez fixed_in=5.80-1ubuntu0.2 username=thyago lote=true`) ||
		!strings.Contains(log.String(), `msg="lote de aprovação" aprovados=2 ja_aprovados=0 fora=0 username=thyago`) {
		t.Errorf("log:\n%s", log)
	}
	if b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "2 itens aprovados em lote.") {
		t.Errorf("aviso do lote:\n%s", b)
	}

	// Itens velhos: já aprovado, não mais pendente, e um valor fora do formato.
	l = local(pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"confirmar": {"1"},
		"sel": {curl, sel("nada", "1"), "%%%", sel("rsync", "3.4.1+ds1-7ubuntu0.3")}}))
	if l != "/painel/patch?fora=2&ja=0&lote=1" {
		t.Errorf("velhos: %s", l)
	}
	if ap := aprovados(); len(ap) != 3 || ap["curl"].Note != "piloto ok" {
		t.Errorf("a aprovação original fica: %+v", ap)
	}
	if b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "1 item aprovado em lote. 2 ficaram de fora: não estão mais aguardando.") {
		t.Errorf("aviso dos velhos:\n%s", b)
	}

	// Pelo filtro: só os aguardando do filtro, e a conferência fecha a lista.
	b = pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"filtro": {"1"}, "q": {"CVE-2026"}}).Body.String()
	if !strings.Contains(b, "Aprovar 2 itens") || !strings.Contains(b, `<code>CVE-2026-1001</code>`) || strings.Contains(b, "<code>curl</code>") ||
		!strings.Contains(b, `<input type="hidden" name="q" value="CVE-2026">`) {
		t.Errorf("conferência do filtro:\n%s", b)
	}

	// O filtro com confirmar=1 forjado ainda passa pela conferência.
	if r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"filtro": {"1"}, "confirmar": {"1"}}); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), "Aprovar 2 itens") || len(aprovados()) != 3 {
		t.Errorf("filtro forjado: %d", r.Code)
	}

	// Nada marcado: volta com o erro.
	if l := local(pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"q": {"curl"}})); l != "/painel/patch?erro=lote-vazio&q=curl" {
		t.Errorf("vazio: %s", l)
	} else if b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Nenhum item aguardando aprovação foi marcado.") {
		t.Errorf("aviso do vazio:\n%s", b)
	}

	// Mais marcados que o limite: recusado inteiro.
	muitos := make([]string, 201)
	for i := range muitos {
		muitos[i] = sel(fmt.Sprintf("CVE-2026-%d", 9000+i), "x")
	}
	if l := local(pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"sel": muitos})); l != "/painel/patch?erro=lote-grande" {
		t.Errorf("lote grande: %s", l)
	}
}

// O filtro com mais itens que o limite: a conferência leva os primeiros, pela ordem da
// lista (a CVE explorada e crítica primeiro), e diz quantos o filtro tinha.
// "Todos do filtro" não tem limite (2.000 máquinas, 10.000 pendências): a conferência mostra o
// resumo e as primeiras 200 linhas e leva a impressão digital da lista, não a lista; no clique,
// o servidor refaz o filtro e só aprova se a lista for a mesma.
func TestLoteDoFiltroSemLimite(t *testing.T) {
	h, st, _ := ambienteLote(t, 2500)
	b := pagina(h, "GET", "/painel/patch?q=CVE", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Conferir todos os 2502 aguardando deste filtro") || !strings.Contains(b, "sem limite") {
		t.Errorf("lista:\n%.3000s", b)
	}
	r := pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, url.Values{"filtro": {"1"}, "q": {"CVE"}})
	b = r.Body.String()
	if !strings.Contains(b, "2502 itens serão aprovados") || !strings.Contains(b, "Aprovar 2502 itens") || !strings.Contains(b, "os outros 2302 entram no mesmo lote") ||
		strings.Count(b, `name="sel"`) != 0 || strings.Count(b, "<tr><td>") != 200 || !strings.Contains(b, `<input type="hidden" name="filtro" value="1">`) {
		t.Fatalf("conferência: %d\n%.4000s", r.Code, b)
	}
	resumo := b[strings.Index(b, `name="resumo" value="`)+len(`name="resumo" value="`):]
	resumo = resumo[:strings.Index(resumo, `"`)]
	confirma := func(res string) *httptest.ResponseRecorder {
		f := url.Values{"filtro": {"1"}, "q": {"CVE"}, "confirmar": {"1"}, "nota": {"lote de novembro"}}
		if res != "" {
			f.Set("resumo", res)
		}
		return pagina(h, "POST", "/painel/patch/aprovar-lote", sessaoAdmin, f)
	}
	// confirmar=1 sem a impressão (forjado) volta para a conferência; com outra, avisa que mudou.
	if r := confirma(""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Aprovar 2502 itens") {
		t.Errorf("sem resumo: %d", r.Code)
	}
	if r := confirma("0123456789abcdef0123456789abcdef"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "A lista do filtro mudou desde a conferência") {
		t.Errorf("resumo errado: %d", r.Code)
	}
	if l, _ := st.PatchApprovals(t.Context()); len(l) != 0 {
		t.Fatalf("gravou antes da hora: %d", len(l))
	}
	if l := local(confirma(resumo)); l != "/painel/patch?fora=0&ja=0&lote=2502&q=CVE" {
		t.Errorf("aprovar todos: %s", l)
	}
	l, _ := st.PatchApprovals(t.Context())
	if len(l) != 2502 || l[0].Note != "lote de novembro" {
		t.Errorf("gravados: %d", len(l))
	}
	if b := pagina(h, "GET", "/painel/patch?q=CVE&status=aguardando", sessaoAdmin, nil).Body.String(); strings.Contains(b, "Conferir todos os") {
		t.Errorf("ainda sobrou:\n%.2000s", b)
	}
}
