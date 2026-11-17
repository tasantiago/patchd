package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// A aba Reinício diz por que reiniciar (os sinais da última busca), o que foi instalado desde
// a última inicialização (o histórico do Windows Update e os programas do patchd) e quem pediu
// o reinício marcado. Dados fictícios.
func TestReinicioDizPorqueEOQue(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithThirdPartyManifest(thirdparty.NewLive(m)))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	_, _ = st.SaveInventory(ctx, maq.MachineID, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Edition: "Professional", Arch: "amd64"}})
	agora := time.Now().UTC()
	boot := agora.Add(-48 * time.Hour)
	sim := true
	_ = st.SaveScan(ctx, maq.MachineID, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: agora,
		Reboot: &protocol.RebootStatus{Pending: &sim, LastBoot: &boot, Signals: []protocol.RebootSignal{
			{Source: "WUA ISystemInformation.RebootRequired", Pending: true},
			{Source: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, Pending: true},
			{Source: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, Pending: true},
			{Source: `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\PendingFileRenameOperations`, Weak: true},
		}},
		History: []protocol.UpdateHistoryEntry{
			{Date: agora.Add(-2 * time.Hour), Operation: "install", Result: "succeeded", Title: "Atualização cumulativa de exemplo (KB5099001)"},
			{Date: agora.Add(-3 * time.Hour), Operation: "install", Result: "failed", Title: "Atualização que falhou (KB5099002)"},
			{Date: agora.Add(-72 * time.Hour), Operation: "install", Result: "succeeded", Title: "Antes do último início (KB5099003)"},
		}})
	// Um programa instalado pelo patchd depois do último início, e o reinício marcado pela campanha.
	tp, _ := json.Marshal(jobs.ThirdPartyParams{App: "7zip", Version: "26.06", SHA256: strings.Repeat("a", 64), Size: 10, Installer: "exe", Args: []string{"/S"}})
	rec, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: maq.MachineID, Type: jobs.TypeThirdParty, TTL: time.Hour, By: "campanha 7", Params: tp}, agora)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishJob(ctx, rec.ID, jobs.StateDone, "código 0", agora.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rb, _ := json.Marshal(jobs.RebootParams{DelayMinutes: 15})
	if _, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: maq.MachineID, Type: jobs.TypeReboot, TTL: 2 * time.Hour, By: "campanha 7", Params: rb}, agora); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(protocol.SelfServiceUser{Account: `EXEMPLO\ana`, Groups: []string{}})
	r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, body)
	var c protocol.SelfServiceCatalog
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &c) != nil || c.Reboot == nil {
		t.Fatalf("loja: %d %s", r.Code, r.Body)
	}
	rbs := c.Reboot
	if len(rbs.Reasons) != 2 || !strings.Contains(rbs.Reasons[0], "Windows Update") || !strings.Contains(rbs.Reasons[1], "Componentes do Windows") {
		t.Errorf("motivos: %q", rbs.Reasons)
	}
	if rbs.By != "a campanha de atualizações da equipe de TI" || rbs.LastBoot == nil || !rbs.LastBoot.Equal(boot) {
		t.Errorf("quem pediu / último início: %q %v", rbs.By, rbs.LastBoot)
	}
	if len(rbs.Updates) != 2 || rbs.Updates[0].Name != "7-Zip 26.06" || rbs.Updates[0].Source != "patchd" ||
		rbs.Updates[1].Name != "Atualização cumulativa de exemplo (KB5099001)" || rbs.Updates[1].Source != "Windows Update" {
		t.Errorf("atualizações: %+v", rbs.Updates)
	}
}
