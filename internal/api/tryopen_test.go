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
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Tentar com o programa aberto, na página do programa: o admin liga; o manifesto vivo troca na
// hora (os próximos jobs levam a escolha), a página e a auditoria dizem; desligar volta.
func TestTentarComOProgramaAbertoNoPainel(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {{AppID: "vscode", Program: "Visual Studio Code",
			Name: "Microsoft Visual Studio Code", State: "DESATUALIZADO", Installed: "1.123.0", Reference: "1.141.0"}}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	abreSessao(t, st, sessaoTeste, agora)
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	m, _ := thirdparty.Default()
	live := thirdparty.NewLive(m)
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(live))
	const pag = "/painel/patch/programa?app=vscode"
	if b := pagina(h, "GET", pag, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `action="/painel/patch/terceiros/tentar-aberto"`) ||
		!strings.Contains(b, "com Code.exe aberto, não instala (adia)") {
		t.Fatal("sem a opção")
	}
	r := pagina(h, "POST", "/painel/patch/terceiros/tentar-aberto", sessaoAdmin, url.Values{"app": {"vscode"}, "ligado": {"1"}})
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "tentar-aberto=1") {
		t.Fatalf("ligar: %d %s", r.Code, local(r))
	}
	tryOpen := func() bool {
		for _, a := range live.For("windows") {
			if a.ID == "vscode" {
				return a.Install.TryOpen
			}
		}
		return false
	}
	if !tryOpen() {
		t.Error("o manifesto vivo não ligou")
	}
	volta, _, _ := strings.Cut(local(r), "#") // o navegador não manda o fragmento
	b := pagina(h, "GET", volta, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "com Code.exe aberto, tenta instalar e só adia se não der certo") ||
		!strings.Contains(b, `name="ligado" value="1" checked`) || !strings.Contains(b, "Ligado: com o programa aberto") {
		t.Error("página depois de ligar")
	}
	pagina(h, "POST", "/painel/patch/terceiros/tentar-aberto", sessaoAdmin, url.Values{"app": {"vscode"}})
	if tryOpen() {
		t.Error("não desligou")
	}
	// O programa sem processos não liga; o leitor não muda nada.
	if loc := local(pagina(h, "POST", "/painel/patch/terceiros/tentar-aberto", sessaoAdmin, url.Values{"app": {"powershell-7"}, "ligado": {"1"}})); !strings.Contains(loc, "erro=tentar-aberto") {
		t.Errorf("sem processos: %s", loc)
	}
	// O programa sem a lista de processos (o Chrome do manifesto): a página diz por que não há a opção.
	if b := pagina(h, "GET", "/painel/patch/programa?app=google-chrome", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não tem a lista de processos") {
		t.Error("sem processos: sem a explicação")
	}
	if r := pagina(h, "POST", "/painel/patch/terceiros/tentar-aberto", sessaoTeste, url.Values{"app": {"vscode"}, "ligado": {"1"}}); r.Code != http.StatusForbidden || tryOpen() {
		t.Errorf("leitura: %d", r.Code)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "terceiros.tentar_aberto"}); len(ev) != 2 {
		t.Errorf("auditoria: %d", len(ev))
	}
}
