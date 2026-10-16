package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	guidCU  = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	guidNET = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
)

func TestWinUpdateParams(t *testing.T) {
	ok := `{"updates":[{"id":"` + guidCU + `","kb":"5127648"},{"id":"` + strings.ToUpper(guidNET) + `"}]}`
	p, err := ParseWinUpdateParams(json.RawMessage(ok))
	if err != nil || len(p.Updates) != 2 || strings.Join(p.Labels(), ",") != "KB5127648,A1B2C3D4" {
		t.Fatalf("válido: %+v %v", p, err)
	}
	ruins := map[string]string{
		"vazio":          `{"updates":[]}`,
		"aspa no GUID":   `{"updates":[{"id":"0b9b6f0c' or IsInstalled=1 or UpdateID='x"}]}`,
		"KB com letras":  `{"updates":[{"id":"` + guidCU + `","kb":"KB5127648"}]}`,
		"repetido":       `{"updates":[{"id":"` + guidCU + `"},{"id":"` + strings.ToUpper(guidCU) + `"}]}`,
		"campo estranho": `{"updates":[{"id":"` + guidCU + `"}],"reboot":true}`,
	}
	for nome, raw := range ruins {
		if _, err := ParseWinUpdateParams(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
	var muitos []string
	for i := range MaxWinUpdates + 1 {
		muitos = append(muitos, fmt.Sprintf(`{"id":"%08d-0000-4000-8000-000000000000"}`, i))
	}
	if _, err := ParseWinUpdateParams(json.RawMessage(`{"updates":[` + strings.Join(muitos, ",") + `]}`)); err == nil {
		t.Error("mais que o limite")
	}
	// O tipo entra na lista fechada, e a assinatura confere os parâmetros.
	if !KnownType(TypeWinUpdate) || checkParams(Job{Type: TypeWinUpdate}) == nil {
		t.Error("windows_update sem parâmetros não vale")
	}
}

func buscaWindows() protocol.PatchScanReport {
	return protocol.PatchScanReport{Scans: []protocol.ScanResult{
		{Source: "wua-default", Missing: []protocol.MissingUpdate{
			{ID: guidCU, KBs: []string{"5127648"}, Title: "Atualização cumulativa", Source: "wua-default"},
		}},
		{Source: "wua-offline", Missing: []protocol.MissingUpdate{
			{ID: guidCU, KBs: []string{"5127648"}, Source: "wua-offline"},
			{ID: guidNET, KBs: []string{"5130001"}, Source: "wua-offline"},
		}},
	}}
}

func TestWinUpdatesForKBs(t *testing.T) {
	p, nao, soOff := WinUpdatesForKBs(buscaWindows(), []string{"KB5127648", "5130001", "5999999"})
	if len(p.Updates) != 2 || p.Updates[0].ID != guidCU || p.Updates[0].KB != "5127648" || p.Updates[1].ID != guidNET {
		t.Errorf("atualizações: %+v", p.Updates)
	}
	if strings.Join(nao, ",") != "5999999" || strings.Join(soOff, ",") != "5130001" {
		t.Errorf("não encontrados %v, só no catálogo offline %v", nao, soOff)
	}
}

func TestStillMissing(t *testing.T) {
	p := WinUpdateParams{Updates: []WinUpdate{{ID: guidCU, KB: "5127648"}, {ID: guidNET}}}
	if still, f := StillMissing(p, buscaWindows()); f != "" || strings.Join(still, ",") != "KB5127648" {
		t.Errorf("pendentes: %v %q", still, f)
	}
	rep := buscaWindows()
	rep.Scans[0].Missing, rep.Scans[0].Error = nil, "0x8024402C"
	if _, f := StillMissing(p, rep); !strings.Contains(f, "falhou (0x8024402C)") {
		t.Errorf("busca que falhou não prova nada: %q", f)
	}
}

func TestPolicySourceProblem(t *testing.T) {
	rep := buscaWindows()
	if p := PolicySourceProblem(rep); p != "" {
		t.Errorf("respondeu: %q", p)
	}
	rep.Scans[0].Missing, rep.Scans[0].Error = nil, "Search: HRESULT 0x80244007"
	if p := PolicySourceProblem(rep); p != "a busca na fonte da política (WSUS ou Microsoft) falhou: Search: HRESULT 0x80244007" {
		t.Errorf("falhou: %q", p)
	}
	if p := PolicySourceProblem(protocol.PatchScanReport{}); p == "" {
		t.Error("sem a fonte")
	}
}

func TestCampanhaDoWindowsHelpers(t *testing.T) {
	for in, want := range map[string]string{"KB5127648": "KB5127648", "kb5127648": "KB5127648", " 5127648 ": "KB5127648"} {
		if got, ok := NormalizeKB(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, ruim := range []string{"", "KB", "KB12", "5127648; rm", "KB5127648x"} {
		if _, ok := NormalizeKB(ruim); ok {
			t.Errorf("%q deveria recusar", ruim)
		}
	}
	items := []protocol.PendingItem{{ID: "CVE-1", FixedIn: "26300.9711 (KB5127648)"}, {ID: "CVE-2", FixedIn: "26300.9711 (KB5127648)"}}
	if got := KBsInPending(items, []string{"KB5130001", "KB5127648", "5127648"}); strings.Join(got, ",") != "5127648" {
		t.Errorf("KBs na pendência: %v", got)
	}
	rep := buscaWindows()
	if p, skip := PolicyWinUpdates(rep, []string{"5127648"}); skip != "" || len(p.Updates) != 1 || p.Updates[0].ID != guidCU {
		t.Errorf("pendente: %+v %q", p, skip)
	}
	if _, skip := PolicyWinUpdates(rep, []string{"5130001"}); skip != "só no catálogo offline" {
		t.Errorf("offline: %q", skip)
	}
	if _, skip := PolicyWinUpdates(rep, []string{"5999999"}); skip != "KB fora da busca" {
		t.Errorf("fora: %q", skip)
	}
	rep.Scans[0].Missing = nil
	if _, skip := PolicyWinUpdates(rep, []string{"5127648"}); skip != "fonte da política falhando" {
		t.Errorf("fonte: %q", skip)
	}
}
