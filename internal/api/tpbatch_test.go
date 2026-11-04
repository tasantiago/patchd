package api_test

import (
	"context"
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
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func TestTerceirosEmLote(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {
			{AppID: "google-chrome", Program: "Google Chrome", Name: "Google Chrome", State: "DESATUALIZADO", Installed: "150.0.1", Reference: "155.0.8059.40"},
			{AppID: "7zip", Program: "7-Zip", Name: "7-Zip", State: "DESATUALIZADO", Installed: "24.01", Reference: "26.04"},
			{AppID: "notepad-plus-plus", Program: "Notepad++", Name: "Notepad++", State: "DESATUALIZADO", Installed: "8.1", Reference: "8.9"},
			{AppID: "vscode", Program: "Visual Studio Code", Name: "VS Code", State: "DESATUALIZADO", Installed: "1.80", Reference: "1.99"},
			{AppID: "vcredist-2008", Program: "Visual C++ 2008", Name: "VC 2008", State: "FORA DE SUPORTE", Installed: "9.0"},
		}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	abreSessao(t, st, sessaoTeste, agora)
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	guarda := func(app, v string) {
		st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: app, WingetID: app, Version: v, Architecture: "x64",
			Type: "exe", URL: "https://x", SHA256: strings.Repeat("a", 64), Size: 1 << 20, Arguments: "/S"})
	}
	// Chrome: duas guardadas, a antiga aprovada; 7-Zip: uma, aguardando; Notepad++: aprovada; VS Code: nada guardado.
	guarda("google-chrome", "154.0.7000.1")
	guarda("google-chrome", "155.0.8059.40")
	guarda("7zip", "26.04")
	guarda("notepad-plus-plus", "8.9")
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "google-chrome", Version: "154.0.7000.1", ApprovedBy: "ana"})
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "notepad-plus-plus", Version: "8.9", ApprovedBy: "ana"})
	m, _ := thirdparty.Default()
	h := api.New(st, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(&m))

	b := pagina(h, "GET", "/painel/patch?aba=terceiros", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{
		"Instaladores aguardando aprovação</span><span class=\"kpi-val v-laranja\">2</span>",
		`<span class="estado reboot_pendente">aguardando 155.0.8059.40</span> <span class="muted">aprovada: 154.0.7000.1</span>`,
		`<span class="estado reboot_pendente">aguardando 26.04</span>`,
		`<span class="estado em_dia">aprovado 8.9</span>`,
		`<span class="estado sem_dado_recente">nada guardado</span>`,
		`<span class="muted">o patchd não instala</span>`,
		`action="/painel/patch/terceiros/aprovar-lote"`, "Conferir todos os 2 aguardando",
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("lista: faltou %q", tr)
		}
	}
	if strings.Count(b, `name="sel"`) != 2 {
		t.Errorf("caixas: %d", strings.Count(b, `name="sel"`))
	}
	// Leitura não tem o lote, nem consegue aprovar.
	if b := pagina(h, "GET", "/painel/patch?aba=terceiros", sessaoTeste, nil).Body.String(); strings.Contains(b, "aprovar-lote") {
		t.Error("leitura com o formulário do lote")
	}
	if r := pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoTeste, url.Values{"filtro": {"1"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}

	// Filtros: instalador e nome; por página.
	b = pagina(h, "GET", "/painel/patch?aba=terceiros&instalador=aguardando", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">Google Chrome</a>") || !strings.Contains(b, ">7-Zip</a>") || strings.Contains(b, ">Notepad++</a>") || !strings.Contains(b, "1–2 de 2") {
		t.Errorf("filtro aguardando:\n%s", b)
	}
	b = pagina(h, "GET", "/painel/patch?aba=terceiros&q=zip", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">7-Zip</a>") || strings.Contains(b, ">Google Chrome</a>") {
		t.Error("busca por nome")
	}
	b = pagina(h, "GET", "/painel/patch?aba=terceiros&instalador=manual", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">Visual C&#43;&#43; 2008</a>") || strings.Contains(b, "aprovar-lote") {
		t.Error("filtro manual")
	}

	// Conferência das marcadas: o 7-Zip e uma versão inventada (fica de fora).
	sel := func(app, v string) string { return url.Values{"app": {app}, "versao": {v}}.Encode() }
	form := url.Values{"sel": {sel("7zip", "26.04"), sel("7zip", "99")}, "q": {"zip"}}
	r := pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoAdmin, form)
	b = r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "1 versão será aprovada") || !strings.Contains(b, "1 marcado(s) ficaram de fora") ||
		!strings.Contains(b, `<input type="hidden" name="q" value="zip">`) {
		t.Fatalf("conferência: %d\n%s", r.Code, b)
	}
	if ap, _ := st.ThirdPartyApprovals(ctx); len(ap) != 2 {
		t.Fatalf("a conferência gravou: %+v", ap)
	}
	form.Set("confirmar", "1")
	form.Set("nota", "piloto ok")
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoAdmin, form)); l != "/painel/patch?aba=terceiros&fora=1&ja=0&lote=1&q=zip" {
		t.Errorf("aprovar: %s", l)
	}
	b = pagina(h, "GET", "/painel/patch?aba=terceiros&lote=1&ja=0&fora=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "1 versão aprovada em lote. 1 ficou de fora") || !strings.Contains(b, `<span class="estado em_dia">aprovado 26.04</span>`) {
		t.Errorf("depois:\n%s", b)
	}

	// Todas do filtro: sobra o Chrome, na versão mais nova; confirmar=1 forjado volta para a conferência.
	r = pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoAdmin, url.Values{"filtro": {"1"}, "confirmar": {"1"}})
	b = r.Body.String()
	if !strings.Contains(b, "1 versão será aprovada") || !strings.Contains(b, "<code>155.0.8059.40</code></td><td><code>154.0.7000.1</code>") {
		t.Fatalf("todas do filtro:\n%s", b)
	}
	local(pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoAdmin, url.Values{"sel": {sel("google-chrome", "155.0.8059.40")}, "confirmar": {"1"}}))
	ap, _ := st.ThirdPartyApprovals(ctx)
	if len(ap) != 4 {
		t.Fatalf("aprovações: %+v", ap)
	}
	for _, a := range ap {
		if a.AppID == "7zip" && (a.ApprovedBy != "thyago" || a.Note != "piloto ok") {
			t.Errorf("7-Zip: %+v", a)
		}
	}
	// Nada aguardando: lote vazio.
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/aprovar-lote", sessaoAdmin, url.Values{"filtro": {"1"}})); !strings.Contains(l, "erro=lote-vazio") {
		t.Errorf("vazio: %s", l)
	}
}
