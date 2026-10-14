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

func TestAprovacaoDeInstaladores(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {
			{AppID: "google-chrome", Program: "Google Chrome", Name: "Google Chrome", State: "DESATUALIZADO", Installed: "150.0.1", Reference: "155.0.8059.40"},
			{AppID: "vcredist-2008", Program: "Visual C++ 2008", Name: "VC 2008", State: "FORA DE SUPORTE", Installed: "9.0"},
		}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	abreSessao(t, st, sessaoTeste, agora)
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	for _, v := range []string{"154.0.7000.1", "155.0.8059.40"} {
		st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "google-chrome", WingetID: "Google.Chrome", Version: v, Architecture: "x64",
			Type: "wix", URL: "https://x", SHA256: strings.Repeat(v[:1], 64), Size: 170 << 20, Arguments: "/quiet /norestart"})
	}
	m, _ := thirdparty.Default()
	log := &syncBuffer{}
	h := api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(&m))
	prog := "/painel/patch/programa?app=google-chrome"

	// Leitura: o cartão, sem formulários.
	b := pagina(h, "GET", prog, sessaoTeste, nil).Body.String()
	for _, tr := range []string{"<h2>Instaladores guardados</h2>", "winget <code>Google.Chrome</code> · assinatura exigida: Google LLC",
		"wix · x64 · 170,0 MiB · <code>111111111111</code>", "Aprovar exige o perfil admin"} {
		if !strings.Contains(b, tr) {
			t.Errorf("leitura: faltou %q", tr)
		}
	}
	if strings.Contains(b, "/painel/patch/terceiros/aprovar") || strings.Index(b, "155.0.8059.40</code></td>") > strings.Index(b, "154.0.7000.1</code></td>") {
		t.Errorf("leitura: formulário ou ordem (a mais nova primeiro):\n%s", b)
	}
	form := url.Values{"app": {"google-chrome"}, "version": {"155.0.8059.40"}, "nota": {"piloto ok"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/aprovar", sessaoTeste, form); r.Code != http.StatusForbidden {
		t.Errorf("leitura aprovando: %d", r.Code)
	}

	// Admin aprova; de novo, a original fica; versão não guardada e nota longa são recusadas.
	aprova := func(f url.Values) string {
		return local(pagina(h, "POST", "/painel/patch/terceiros/aprovar", sessaoAdmin, f))
	}
	if l := aprova(form); l != prog+"&aprovado=1" {
		t.Fatalf("aprovar: %s", l)
	}
	if l := aprova(form); l != prog+"&erro=ja-aprovado" {
		t.Errorf("de novo: %s", l)
	}
	if l := aprova(url.Values{"app": {"google-chrome"}, "version": {"999"}}); l != prog+"&erro=nao-guardado" {
		t.Errorf("não guardado: %s", l)
	}
	if l := aprova(url.Values{"app": {"google-chrome"}, "version": {"154.0.7000.1"}, "nota": {strings.Repeat("x", 501)}}); l != prog+"&erro=nota" {
		t.Errorf("nota: %s", l)
	}
	// Programa sem install na regra (o VC++ 2008 é "fora de suporte"): volta para a lista.
	if l := aprova(url.Values{"app": {"vcredist-2008"}, "version": {"9.0"}}); l != "/painel/patch?aba=terceiros" {
		t.Errorf("sem install: %s", l)
	}
	ap, _ := st.ThirdPartyApprovals(ctx)
	if len(ap) != 1 || ap[0].ApprovedBy != "thyago" || ap[0].Note != "piloto ok" {
		t.Fatalf("gravado: %+v", ap)
	}
	if !strings.Contains(log.String(), `msg="instalador de terceiros aprovado" app=google-chrome version=155.0.8059.40 username=thyago`) {
		t.Errorf("log:\n%s", log)
	}
	b = pagina(h, "GET", prog+"&aprovado=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Versão aprovada") || !strings.Contains(b, `<span class="estado em_dia">aprovado</span> <span class="muted">thyago · `) ||
		!strings.Contains(b, `action="/painel/patch/terceiros/revogar"`) || !strings.Contains(b, `action="/painel/patch/terceiros/aprovar"`) {
		t.Errorf("admin depois de aprovar:\n%s", b)
	}

	// Revogar.
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/revogar", sessaoAdmin, url.Values{"app": {"google-chrome"}, "version": {"155.0.8059.40"}})); l != prog+"&revogado=1" {
		t.Errorf("revogar: %s", l)
	}
	if ap, _ := st.ThirdPartyApprovals(ctx); len(ap) != 0 || !strings.Contains(log.String(), "aprovação de instalador de terceiros revogada") {
		t.Errorf("revogada: %+v", ap)
	}
	// O programa sem install não tem o cartão.
	if b := pagina(h, "GET", "/painel/patch/programa?app=vcredist-2008", sessaoAdmin, nil).Body.String(); strings.Contains(b, "Instaladores guardados") {
		t.Error("cartão num programa sem install")
	}
}
