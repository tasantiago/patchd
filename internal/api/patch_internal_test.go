package api

import (
	"strings"
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

// Muitas máquinas com a MESMA lista de itens (o mesmo build do Windows, compartilhada pelo
// serviço de compliance): a junção dá o mesmo resultado que com listas próprias, as máquinas
// de cada item saem na ordem do nome, e o resumo por correção conta cada máquina uma vez.
func TestAgregacaoComListasCompartilhadas(t *testing.T) {
	kb1, kb2 := "26200.7000 (KB5070000)", "26200.7100 (KB5070100)"
	comum := []protocol.PendingItem{
		{ID: "CVE-2026-1", FixedIn: kb1, Severity: "Important", Installed: "26200.6900"},
		{ID: "CVE-2026-2", FixedIn: kb1, Severity: "Critical", Exploited: true, Installed: "26200.6900"},
		{ID: "CVE-2026-3", FixedIn: kb2, Severity: "Important", Installed: "26200.6900"},
	}
	so := []protocol.PendingItem{{ID: "CVE-2026-3", FixedIn: kb2, Severity: "Important", Installed: "26200.7000"}}
	win := func(id, name string, items []protocol.PendingItem) protocol.MachineItems {
		return protocol.MachineItems{MachineID: id, Hostname: name, OS: "Windows 11 Pro 25H2", Catalog: "Windows 11 25H2", Unit: "CVEs", Items: items}
	}
	nomes := []string{"PC-Zeta", "pc-alfa", "PC-Mike", "pc-beta", "PC-Eco"}
	var juntas, proprias []protocol.MachineItems
	for i, n := range nomes {
		id := string(rune('a'+i)) + "-id"
		juntas = append(juntas, win(id, n, comum))
		proprias = append(proprias, win(id, n, append([]protocol.PendingItem(nil), comum...)))
	}
	juntas = append(juntas, win("x-id", "pc-fox", so), protocol.MachineItems{MachineID: "v-id", Hostname: "vazia", Unit: "CVEs"})
	proprias = append(proprias, win("x-id", "pc-fox", so), protocol.MachineItems{MachineID: "v-id", Hostname: "vazia", Unit: "CVEs"})

	a, b := aggregateItems(juntas, nil), aggregateItems(proprias, nil)
	if len(a) != 3 || len(b) != 3 {
		t.Fatalf("3 itens: %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].FixedIn != b[i].FixedIn || a[i].Severity != b[i].Severity || a[i].Exploited != b[i].Exploited ||
			len(a[i].Machines) != len(b[i].Machines) {
			t.Fatalf("item %d difere: %+v\n%+v", i, a[i], b[i])
		}
		for j := range a[i].Machines {
			x, y := a[i].Machines[j], b[i].Machines[j]
			if x.ID != y.ID || x.Name != y.Name || x.Installed != y.Installed {
				t.Errorf("item %s, máquina %d: %+v e %+v", a[i].ID, j, x, y)
			}
		}
	}
	if a[0].ID != "CVE-2026-2" || len(a[0].Machines) != 5 {
		t.Fatalf("a explorada primeiro, em 5 máquinas: %+v", a[0])
	}
	var ordem []string
	for _, m := range a[0].Machines {
		ordem = append(ordem, m.Name)
	}
	if got := strings.Join(ordem, " "); got != "pc-alfa pc-beta PC-Eco PC-Mike PC-Zeta" {
		t.Errorf("máquinas na ordem do nome, sem olhar a caixa: %s", got)
	}
	var tres patchItem
	for _, p := range a {
		if p.ID == "CVE-2026-3" {
			tres = p
		}
	}
	if len(tres.Machines) != 6 || tres.Machines[3].Name != "pc-fox" || tres.Machines[3].Installed != "26200.7000" {
		t.Errorf("CVE-2026-3 (das duas listas): %+v", tres.Machines)
	}

	// O resumo por correção: o KB de duas CVEs conta as 5 máquinas uma vez só; o outro, as 6.
	fs := waitingFixes(a)
	if fs.cves != 3 || fs.pkg != 0 || len(fs.list) != 2 || len(fs.machines) != 6 {
		t.Fatalf("resumo: %+v", fs)
	}
	if f := fs.list[0]; f.FixedIn != kb1 || f.Items != 2 || f.Machines != 5 || f.Exploited != 1 || f.Systems != "Windows" {
		t.Errorf("KB das duas CVEs: %+v", f)
	}
	if f := fs.list[1]; f.FixedIn != kb2 || f.Items != 1 || f.Machines != 6 {
		t.Errorf("KB de uma CVE: %+v", f)
	}
	if fs.machines["a-id"].N != 3 || fs.machines["x-id"].N != 1 || fs.machines["a-id"].Name != "PC-Zeta" {
		t.Errorf("itens por máquina: %+v %+v", fs.machines["a-id"], fs.machines["x-id"])
	}
	// Aprovado sai do resumo.
	apr := withApprovals(a, []store.PatchApproval{{Item: "CVE-2026-3", FixedIn: kb2, ApprovedBy: "ana"}})
	if fs := waitingFixes(apr); len(fs.list) != 1 || fs.cves != 2 || fs.machines["x-id"] != nil {
		t.Errorf("resumo sem o aprovado: %+v", fs)
	}
}
