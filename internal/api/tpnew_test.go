package api_test

import (
	"context"
	"log/slog"
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

// O aviso de versão nova: a referência passou a guardada mais nova (para baixar), ou a guardada
// mais nova espera aprovação. Aparece no menu (Terceiros), na lista (indicador, aviso, filtro e
// a marca na linha), na página do programa e na Visão geral. O programa sem nada guardado (não
// usado) e a referência que não se compara não avisam.
func TestAvisoDeVersaoNova(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {
			{AppID: "7zip", Program: "7-Zip", Name: "7-Zip", State: "DESATUALIZADO", Installed: "24.01", Reference: "26.06"},
			{AppID: "notepad-plus-plus", Program: "Notepad++", Name: "Notepad++", State: "DESATUALIZADO", Installed: "8.1", Reference: "8.9"},
			{AppID: "vscode", Program: "Visual Studio Code", Name: "VS Code", State: "DESATUALIZADO", Installed: "1.80", Reference: "1.99"},
		}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	guarda := func(app, v string) {
		st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: app, WingetID: app, Version: v, Architecture: "x64",
			Type: "exe", URL: "https://x", SHA256: strings.Repeat("a", 64), Size: 1 << 20, Arguments: "/S"})
	}
	// 7-Zip: guardada e aprovada a 26.04, referência 26.06 (baixar). Notepad++: guardada 8.9
	// sem aprovar, referência 8.9 (aprovar). VS Code: nada guardado (não avisa).
	guarda("7zip", "26.04")
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "ana"})
	guarda("notepad-plus-plus", "8.9")
	st.SaveThirdPartyVersion(ctx, "7zip", "windows", "26.06", "winget:7zip.7zip")
	st.SaveThirdPartyVersion(ctx, "notepad-plus-plus", "windows", "8.9", "winget:Notepad++.Notepad++")
	st.SaveThirdPartyVersion(ctx, "vscode", "windows", "1.99", "winget:Microsoft.VisualStudioCode")
	m, _ := thirdparty.Default()
	h := api.New(st, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(thirdparty.NewLive(m)))

	b := pagina(h, "GET", "/painel/patch?aba=terceiros", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{
		`<span class="cont" title="programas com versão nova para baixar ou aguardando aprovação">2</span>`,
		`Versões novas para baixar</span><span class="kpi-val v-laranja">1</span>`,
		"<b>1</b> programa(s) com versão nova para baixar</a>", "<b>1</b> com instalador guardado aguardando aprovação</a>",
		`href="/painel/patch/programa?app=7zip#instaladores" title="a versão de referência é mais nova que a guardada">baixar 26.06</a>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("lista: faltou %q", tr)
		}
	}
	if strings.Count(b, ">baixar ") != 1 {
		t.Errorf("marcas de baixar: %d", strings.Count(b, ">baixar "))
	}
	// O filtro: só o 7-Zip.
	b = pagina(h, "GET", "/painel/patch?aba=terceiros&todos=1&instalador=baixar", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, ">7-Zip</a>") || strings.Contains(b, ">Notepad&#43;&#43;</a>") || strings.Contains(b, ">Visual Studio Code</a>") {
		t.Error("filtro baixar")
	}
	b = pagina(h, "GET", "/painel/patch/programa?app=7zip", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<b>Versão nova para baixar:</b> a de referência é <code>26.06</code>, e a guardada mais nova é <code>26.04</code>") {
		t.Error("página do programa")
	}
	if b := pagina(h, "GET", "/painel/patch/programa?app=notepad-plus-plus", sessaoAdmin, nil).Body.String(); strings.Contains(b, "Versão nova para baixar") {
		t.Error("aviso de baixar no programa em dia")
	}
	b = pagina(h, "GET", "/painel/patch?aba=geral", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"1 programa(s) de terceiros com versão nova para baixar", "1 programa(s) de terceiros com instalador guardado aguardando aprovação",
		"instalador=baixar"} {
		if !strings.Contains(b, tr) {
			t.Errorf("visão geral: faltou %q", tr)
		}
	}
}
