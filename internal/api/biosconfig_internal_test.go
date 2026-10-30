package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// O "ok" do agente vale; o inventário diz o que já aparece com o valor novo e o que só vale
// depois do reinício.
func TestJulgaConfiguracoesDaBIOS(t *testing.T) {
	st := store.NewMemory()
	a := &api{store: st}
	const id = "maq-1"
	params, _ := jobs.BIOSSettingsParamsJSON(jobs.BIOSSettingsParams{Vendor: "hp", BoxKey: "0123456789ab", Changes: []biosconfig.Change{
		{Name: "Wake On LAN", Value: "Boot to Hard Drive"}, {Name: "Fast Boot", Value: "Disable"}}})
	payload, _ := json.Marshal(jobs.Job{ID: 9, MachineID: id, Type: jobs.TypeBIOSSettings, Params: params})
	job := store.JobRow{ID: 9, MachineID: id, Type: jobs.TypeBIOSSettings, Payload: string(payload)}
	r := httptest.NewRequest("POST", "/", nil)
	res := protocol.JobResult{Status: protocol.JobResultOK, Detail: "Wake On LAN: gravada; Fast Boot: gravada"}

	state, detail := a.judgeBIOSSettings(r, job, res)
	if state != jobs.StateDone || !strings.Contains(detail, "não conseguiu conferir") {
		t.Errorf("sem inventário: %s %s", state, detail)
	}
	_, _ = st.SaveInventory(context.Background(), id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h", Hardware: &protocol.Hardware{
		Firmware: &protocol.Firmware{Interface: "hp", Settings: []protocol.BIOSSetting{
			{Name: "Wake On LAN", Kind: protocol.BIOSKindEnum, Value: "Boot to Hard Drive", Possible: []string{"Disable", "Boot to Hard Drive"}},
			{Name: "Fast Boot", Kind: protocol.BIOSKindEnum, Value: "Enable", Possible: []string{"Disable", "Enable"}}}}}})
	state, detail = a.judgeBIOSSettings(r, job, res)
	if state != jobs.StateDone || !strings.Contains(detail, "conferido no inventário: Wake On LAN") ||
		!strings.Contains(detail, "vale depois do reinício): Fast Boot") || !strings.Contains(detail, "agente: Wake On LAN: gravada") {
		t.Errorf("com inventário: %s %s", state, detail)
	}
}
