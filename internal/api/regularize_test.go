package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// A regularização pela Central de software: o PowerShell instalado pelo bundle só vira
// atualização com o parâmetro ligado e o agente que sabe remover; o job leva a entrada do
// bundle para sair antes do MSI aprovado.
func TestRegularizacaoPelaCentral(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	for _, c := range []struct {
		nome, param string
		caps        []string
		acao        string
	}{
		{"desligada", "0", []string{protocol.CapabilityJobs, protocol.CapabilityRegularize}, "indisponivel"},
		{"agente anterior", "1", []string{protocol.CapabilityJobs}, "indisponivel"},
		{"ligada", "1", []string{protocol.CapabilityJobs, protocol.CapabilityRegularize}, "atualizar"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			dir := t.TempDir()
			h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(thirdparty.NewLive(m)),
				api.WithSettings(parametrosFixos{api.RegularizeKey: c.param}))
			ctx := context.Background()
			tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
			_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
			maq := registra(t, h, tok)
			corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), Capabilities: c.caps})
			if r := envia(h, "POST", "/api/v1/agent/checkin", maq.Credential, corpo); r.Code != http.StatusOK {
				t.Fatalf("check-in: %d", r.Code)
			}
			chave := `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\{0CD7CB36-56F0-49FF-8BE2-1250C8660C0D}`
			_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
				OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
				Software: []protocol.Software{
					{Name: "PowerShell 7.6.6.0-x64", Version: "7.6.6.0", Publisher: "Microsoft Corporation", Scope: "machine",
						BundleUpgradeCode: "{AAAAAAAA-1111-2222-3333-BBBBBBBBBBBB}", Uninstall: protocol.UninstallQuiet, Source: chave},
					{Name: "PowerShell 7-x64", Version: "7.6.6.0", Publisher: "Microsoft Corporation", Scope: "machine",
						ProductCode: "{23170F69-40C1-2702-2602-000001000000}", SystemComponent: true, Uninstall: protocol.UninstallMSI},
				}})
			inst := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte(strings.Repeat("ps", 100))...)
			sum := sha256.Sum256(inst)
			sha := hex.EncodeToString(sum[:])
			_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
			if err := os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, true)), inst, 0o640); err != nil {
				t.Fatal(err)
			}
			_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "powershell-7", Version: "7.6.9.0", Type: "wix", Architecture: "x64",
				SHA256: sha, Size: int64(len(inst)), Arguments: "/quiet /norestart"})
			_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "powershell-7", Version: "7.6.9.0", ApprovedBy: "admin", ApprovedAt: time.Now().UTC()})
			_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "powershell-7", CanUpdate: true, UpdatedBy: "admin"})

			body, _ := json.Marshal(protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{`EXEMPLO\Domain Users`}})
			r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
			var loja protocol.SelfServiceCatalog
			_ = json.Unmarshal(r.Body.Bytes(), &loja)
			if r.Code != http.StatusOK || len(loja.Items) != 1 || loja.Items[0].Action != c.acao || loja.Items[0].Replaces != (c.acao == "atualizar") {
				t.Fatalf("loja: %d %+v", r.Code, loja.Items)
			}
			if c.acao != "atualizar" {
				return
			}
			body, _ = json.Marshal(protocol.SelfServiceRequest{SelfServiceUser: protocol.SelfServiceUser{Account: `EXEMPLO\ana`,
				Groups: []string{`EXEMPLO\Domain Users`}}, App: "powershell-7", Action: "atualizar"})
			var resp protocol.SelfServiceResponse
			_ = json.Unmarshal(envia(h, "POST", protocol.SelfServiceRequestPath, maq.Credential, body).Body.Bytes(), &resp)
			if resp.Status != protocol.SelfServiceCreated {
				t.Fatalf("pedido: %+v", resp)
			}
			j, _, _ := st.JobForMachine(ctx, maq.MachineID, resp.JobID)
			var payload jobs.Job
			_ = json.Unmarshal([]byte(j.Payload), &payload)
			p, err := jobs.ParseThirdPartyParams(payload.Params)
			if err != nil || p.Version != "7.6.9.0" || p.Replace == nil || p.Replace.Key != chave || p.Replace.Method != "quiet" ||
				p.Replace.Name != "PowerShell 7.6.6.0-x64" {
				t.Fatalf("job: %+v %v", p.Replace, err)
			}
		})
	}
}

// Na página da máquina: com a regularização desligada, o programa que ela trocaria (o Chrome
// do exe, com o MSI aprovado mais novo) mostra o caminho para ligá-la; ligada, vira atualização.
func TestRegularizacaoNaPaginaDaMaquina(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	for _, valor := range []string{"0", "1"} {
		h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(t.TempDir(), nil), api.WithThirdPartyManifest(thirdparty.NewLive(m)),
			api.WithSettings(parametrosFixos{api.RegularizeKey: valor}))
		ctx := context.Background()
		const id = "92fc3b93-0003" // uma máquina do ambiente de páginas
		_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w", OS: protocol.OSInfo{ID: "windows", Arch: "amd64"},
			Software: []protocol.Software{{Name: "Google Chrome", Version: "156.0.7000.1", Publisher: "Google LLC", Scope: "machine",
				Source: `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Google Chrome`}}})
		_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "google-chrome", Version: "157.0.8103.79", Architecture: "x64",
			Type: "wix", URL: "https://x", SHA256: strings.Repeat("c", 64), Size: 1, Arguments: "/quiet /norestart"})
		b := pagina(h, "GET", "/painel/maquinas/"+id+"?aba=pendencias", sessaoAdmin, nil).Body.String()
		link := `<a href="` + strings.ReplaceAll(api.RegularizeHref, "&", "&amp;") + `">trocar pelo instalador aprovado: ligar a regularização</a>`
		if valor == "0" && !strings.Contains(b, link) {
			t.Errorf("desligada: sem o caminho para ligar")
		}
		if valor == "1" && (strings.Contains(b, "ligar a regularização") || !strings.Contains(b, "instala por cima")) {
			t.Errorf("ligada: o plano não regulariza")
		}
	}
}
