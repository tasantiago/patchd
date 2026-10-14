package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Campanha de terceiros pelo painel (Aula 14.8): da página do programa ao formulário já
// preenchido, a conferência com a previsão de cada anel, e a criação.
func TestCampanhaDeTerceirosPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	m, _ := thirdparty.Default()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	const pc = "92fc3b93-0003"
	st.SaveInventory(ctx, pc, protocol.InventoryReport{SchemaVersion: 1, Hash: "w", OS: protocol.OSInfo{ID: "windows", Arch: "amd64"},
		Software: []protocol.Software{{Name: "LibreOffice 25.2.1.1", Version: "25.2.1.1", Publisher: "The Document Foundation", Scope: "machine", ProductCode: "{LO}"}}})
	st.SetMachineRing(ctx, pc, 0)
	st.SetMachineRing(ctx, "60ef97e4-0001", 0)
	inst := []byte("MSI do LibreOffice 26.8.1.1")
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	os.WriteFile(filepath.Join(dir, "pacotes", sha+".msi"), inst, 0o640)
	st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "libreoffice", WingetID: "TheDocumentFoundation.LibreOffice", Version: "26.8.1.1",
		Architecture: "x64", Type: "msi", URL: "https://x", SHA256: sha, Size: int64(len(inst)), Arguments: "/quiet /norestart"})
	form := formCampanha("nome", "libreoffice 26.8.1.1", "tipo", "thirdparty", "programa", "libreoffice 26.8.1.1", "anel", "0|1", "observacao", "1")

	// Sem aprovação: nada no select, e o pedido volta com o motivo.
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `value="thirdparty"`) || !strings.Contains(b, "Nenhuma versão aprovada e guardada") {
		t.Errorf("sem aprovação:\n%s", b)
	}
	r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form)
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "A versão não está pronta para campanha: libreoffice 26.8.1.1 não está aprovado") {
		t.Errorf("sem aprovação: %d", r.Code)
	}

	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "libreoffice", Version: "26.8.1.1", ApprovedBy: "ana"})
	// A página do programa leva ao formulário preenchido (só para o admin). Ela precisa
	// da avaliação com o programa: um segundo handler, no mesmo banco.
	src := &complianceFalso{fleet: protocol.ComplianceFleet{Machines: []protocol.ComplianceStatus{{MachineID: pc}}},
		terceiros: map[string][]protocol.ThirdPartyItem{pc: {{AppID: "libreoffice", Program: "LibreOffice", Name: "LibreOffice 25.2.1.1",
			State: "DESATUALIZADO", Installed: "25.2.1.1", Reference: "26.8.1.1"}}}}
	hp := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src), api.WithJobKey(priv), api.WithThirdPartyManifest(&m))
	prog := pagina(hp, "GET", "/painel/patch/programa?app=libreoffice", sessaoAdmin, nil).Body.String()
	if !strings.Contains(prog, `<a class="btn" href="/painel/campanhas?programa=libreoffice&#43;26.8.1.1&amp;tipo=thirdparty#nova">Campanha por anel</a>`) {
		t.Errorf("programa (admin) sem o link:\n%s", prog)
	}
	if strings.Contains(pagina(hp, "GET", "/painel/patch/programa?app=libreoffice", sessaoTeste, nil).Body.String(), "Campanha por anel") {
		t.Error("leitura vê o link")
	}
	b = pagina(h, "GET", "/painel/campanhas?tipo=thirdparty&programa=libreoffice+26.8.1.1", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`<section id="nova">`, `value="thirdparty" checked`, `<option value="libreoffice 26.8.1.1" selected>LibreOffice 26.8.1.1</option>`,
		`value="libreoffice 26.8.1.1" placeholder`} {
		if !strings.Contains(b, tr) {
			t.Errorf("preenchido: faltou %q", tr)
		}
	}

	// Conferência: a previsão por anel; nada criado ainda.
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form).Body.String()
	for _, tr := range []string{"instalar programa de terceiros · programa: libreoffice 26.8.1.1", "<th>Hoje</th>",
		"<td>1 job(s); fora: 1 sem inventário do Windows</td>", `name="programa" value="libreoffice 26.8.1.1"`, "a campanha pausa"} {
		if !strings.Contains(b, tr) {
			t.Errorf("conferência: faltou %q\n", tr)
		}
	}
	if l, _ := st.Campaigns(ctx, 5); len(l) != 0 {
		t.Fatal("a conferência criou campanha")
	}
	form.Set("confirmar", "1")
	if l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form)); l != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %s", l)
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.Type != jobs.TypeThirdParty || c.App != "libreoffice" || c.Version != "26.8.1.1" || c.CreatedBy != "thyago" || len(c.Rings) != 2 {
		t.Errorf("criada: %+v", c)
	}
	if !strings.Contains(log.String(), "programa=\"libreoffice 26.8.1.1\"") {
		t.Errorf("log:\n%s", log)
	}
	// Programa forjado (sem versão): volta ao formulário.
	if r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("tipo", "thirdparty", "programa", "libreoffice", "anel", "0")); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "Escolha o programa e a versão aprovada") {
		t.Errorf("forjado: %d", r.Code)
	}
}
