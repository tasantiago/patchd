package api

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestAgregacaoPorItem(t *testing.T) {
	ubuntu := func(id, name string, items ...protocol.PendingItem) protocol.MachineItems {
		return protocol.MachineItems{MachineID: id, Hostname: name, OS: "Ubuntu 26.04", Catalog: "ubuntu-26.04", Unit: "pacotes", Items: items}
	}
	curl := protocol.PendingItem{ID: "curl", FixedIn: "8.18.0-1ubuntu2.10", Severity: "medium", Installed: "8.18.0-1ubuntu2.7"}
	curlExpl := curl
	curlExpl.Exploited, curlExpl.Severity = true, "high"
	list := []protocol.MachineItems{
		ubuntu("m-3", "zeta", curl),
		ubuntu("m-1", "alfa", curlExpl, protocol.PendingItem{ID: "bluez", FixedIn: "5.80-1", Severity: "low"}),
		ubuntu("m-2", "beta", protocol.PendingItem{ID: "bluez", FixedIn: "5.80-1", Severity: "low"},
			protocol.PendingItem{ID: "curl", FixedIn: "8.18.0-1ubuntu2.11"}), // outra correção do curl: outra linha
		{MachineID: "w-1", Hostname: "win", OS: "Windows 11 Pro 25H2", Catalog: "Windows 11 25H2", Unit: "CVEs",
			Items: []protocol.PendingItem{{ID: "CVE-2026-1", FixedIn: "26200.7000 (KB5070000)", Severity: "Critical"}}},
		{MachineID: "m-4", Hostname: "vazia", Unit: "pacotes"},
	}
	apr := []store.PatchApproval{{Item: "bluez", FixedIn: "5.80-1", ApprovedBy: "ana", ApprovedAt: time.Now()}}
	got := aggregateItems(list, apr)
	if len(got) != 4 {
		t.Fatalf("4 linhas (curl ×2 correções, bluez, CVE): %+v", got)
	}
	// Ordem: explorada primeiro; depois severidade (Critical > low); depois sistemas.
	order := []string{"curl 8.18.0-1ubuntu2.10", "CVE-2026-1 26200.7000 (KB5070000)", "bluez 5.80-1", "curl 8.18.0-1ubuntu2.11"}
	for i, p := range got {
		if p.ID+" "+p.FixedIn != order[i] {
			t.Errorf("%d: %s %s, quer %s", i, p.ID, p.FixedIn, order[i])
		}
	}
	c := got[0]
	if !c.Exploited || c.Severity != "high" || len(c.Machines) != 2 || c.Machines[0].Name != "alfa" || c.Kind != "Pacote" ||
		len(c.Catalogs) != 1 || c.Approval != nil {
		t.Errorf("curl: %+v", c)
	}
	if got[1].Kind != "CVE" || got[2].Approval == nil || got[2].Approval.ApprovedBy != "ana" || len(got[2].Machines) != 2 {
		t.Errorf("CVE e bluez: %+v %+v", got[1], got[2])
	}
	if f := filterPatch(got, patchQuery{Status: "aguardando"}); len(f) != 3 {
		t.Errorf("aguardando: %d", len(f))
	}
	if f := filterPatch(got, patchQuery{Status: "exploradas"}); len(f) != 1 {
		t.Errorf("exploradas: %d", len(f))
	}
	if f := filterPatch(got, patchQuery{Q: "kb5070"}); len(f) != 1 || f[0].ID != "CVE-2026-1" {
		t.Errorf("busca pela correção: %+v", f)
	}
}
