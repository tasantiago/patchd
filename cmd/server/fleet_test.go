package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
)

// bancoFrota acrescenta a lista de máquinas ao banco falso da avaliação.
type bancoFrota struct {
	*bancoComplianceFalso
	maquinas []protocol.MachineSummary
	falha    string // ID cujo inventário dá erro de banco
}

func (b *bancoFrota) Machines(context.Context) ([]protocol.MachineSummary, error) {
	return b.maquinas, nil
}

func (b *bancoFrota) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	if id == b.falha {
		return protocol.InventoryReport{}, false, errors.New("conexão com o banco caiu")
	}
	return b.bancoComplianceFalso.LatestInventory(ctx, id)
}

func TestEstadoDaFrota(t *testing.T) {
	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	coleta := agora.Add(-3 * time.Hour)
	sw := func(name, ver, src string) protocol.Software {
		return protocol.Software{Name: name, Version: ver, SourcePackage: src, SourcePackageVersion: ver}
	}
	ubuntu := func(curl string, kernels ...string) protocol.InventoryReport {
		r := protocol.InventoryReport{SchemaVersion: 1, CollectedAt: coleta,
			OS:       protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04", Kernel: "7.0.0-34-generic"},
			Software: []protocol.Software{sw("curl", curl, "curl")}}
		for _, k := range kernels {
			r.Software = append(r.Software, sw("linux-modules-"+k+"-generic", k+".1", "linux"))
		}
		return r
	}
	win := protocol.InventoryReport{SchemaVersion: 1, CollectedAt: coleta,
		OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: "25H2", Build: "26200.9457", Arch: "amd64", Edition: "Professional"}}
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	sim, nao := true, false
	b := &bancoFrota{
		bancoComplianceFalso: &bancoComplianceFalso{
			inv: map[string]protocol.InventoryReport{
				"ubuntu-pendente": ubuntu("8.18.0-1ubuntu2.7", "7.0.0-34", "7.0.0-36"),
				"ubuntu-parada":   ubuntu("8.18.0-1ubuntu2.10"),
				"win-em-dia":      win,
				"win-reinicio":    win,
				"fedora-velho":    {SchemaVersion: 1, CollectedAt: coleta, OS: protocol.OSInfo{ID: "fedora", Name: "Fedora Linux", Version: "41"}},
				"sem-inventario":  {},
				"com-erro":        win,
			},
			ubuntu: []compliance.Advisory{{ID: "USN-9002-1", Source: "curl", Fixed: "8.18.0-1ubuntu2.10", Published: coleta,
				KEV: []string{"CVE-2026-9999"}, CVEs: []string{"CVE-2026-9999"}}},
			produtos: []compliance.MSRCProduct{{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"}},
			windows: []compliance.WindowsFix{{Document: "2026-Sep", Released: set, CVE: "CVE-2026-50500", KEV: true, KB: "5124008",
				SubType: "Security Update", Build: "10.0.26200.9445"}},
		},
		falha: "com-erro",
	}
	m := func(id, host string, visto time.Duration, reboot *bool) protocol.MachineSummary {
		return protocol.MachineSummary{ID: id, Hostname: host, OSName: "SO", OSVersion: "1", LastSeenAt: agora.Add(-visto), RebootPending: reboot}
	}
	b.maquinas = []protocol.MachineSummary{
		m("com-erro", "pc-erro", time.Hour, nil),
		m("fedora-velho", "pc-fedora", time.Hour, nil),
		m("sem-inventario", "pc-novo", time.Hour, nil),
		m("ubuntu-parada", "pc-parado", 10*24*time.Hour, &nao),
		m("ubuntu-pendente", "pc-ubuntu", 30*time.Minute, &nao),
		m("win-em-dia", "pc-win", time.Hour, &nao),
		m("win-reinicio", "pc-win2", time.Hour, &sim),
	}
	log := &bytes.Buffer{}
	s := &complianceService{st: b, logger: slog.New(slog.NewTextHandler(log, nil)), now: func() time.Time { return agora }}

	fleet, err := s.Fleet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	estados := map[string]string{}
	for _, cs := range fleet.Machines {
		estados[cs.MachineID] = cs.State
	}
	esperado := map[string]compliance.State{
		"ubuntu-pendente": compliance.StateMissing,
		"win-reinicio":    compliance.StateReboot,
		"ubuntu-parada":   compliance.StateStale,
		"fedora-velho":    compliance.StateUnknown,
		"sem-inventario":  compliance.StateUnknown,
		"com-erro":        compliance.StateUnknown,
		"win-em-dia":      compliance.StateOK,
	}
	for id, e := range esperado {
		if estados[id] != string(e) {
			t.Errorf("%s: %s, esperado %s", id, estados[id], e)
		}
	}
	if fleet.Machines[0].MachineID != "ubuntu-pendente" || fleet.Machines[len(fleet.Machines)-1].MachineID != "win-em-dia" {
		t.Errorf("ordem: o que pede ação primeiro, em dia por último: %v", estados)
	}
	if fleet.Counts["faltando"] != 1 || fleet.Counts["desconhecido"] != 3 || fleet.Counts["em_dia"] != 1 || len(fleet.Counts) != 5 {
		t.Errorf("contagens: %v", fleet.Counts)
	}

	u := fleet.Machines[0]
	if u.Pending != 1 || u.PendingUnit != "pacotes" || u.Exploited != 1 || u.Catalog != "Ubuntu:26.04:LTS" || u.InventoryCollectedAt == nil ||
		!strings.Contains(strings.Join(u.Reasons, ";"), "kernel mais novo") {
		t.Errorf("Ubuntu pendente: %+v", u)
	}
	if !strings.Contains(log.String(), "conexão com o banco caiu") {
		t.Error("o erro de uma máquina vai para o log")
	}

	// Uma máquina só.
	if cs, ok, err := s.Machine(context.Background(), "win-em-dia"); err != nil || !ok || cs.State != "em_dia" || cs.Reasons == nil {
		t.Errorf("máquina: %+v %t %v", cs, ok, err)
	}
	if _, ok, _ := s.Machine(context.Background(), "win"); ok {
		t.Error("o ID tem de ser o completo (sem prefixo)")
	}

	// Impressão: a tabela tem nomes; o resumo, não.
	var out bytes.Buffer
	printFleet(fleet, false, &out)
	for _, tr := range []string{
		"Frota: 7 máquina(s): faltando 1 · reboot pendente 1 · sem dado recente 1 · desconhecido 3 · em dia 1",
		"faltando          ubuntu-p  SO 1  1 pacotes",
		"há 30 min", "há 10 dias", "pc-ubuntu",
	} {
		if !strings.Contains(out.String(), tr) {
			t.Errorf("faltou %q em:\n%s", tr, out.String())
		}
	}
	out.Reset()
	printFleet(fleet, true, &out)
	if strings.Contains(out.String(), "pc-") || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("resumo:\n%s", out.String())
	}
}

func TestComplianceAllFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-all", "-machine", "x"},
		{"-resumo", "-machine", "x"},
		{"-all", "-v"},
		{"-all", "-como", "25H2"},
	} {
		var errOut bytes.Buffer
		if code := runCompliance(args, lookMap(nil), io.Discard, &errOut); code != exitConfig {
			t.Errorf("%v: %d %s", args, code, errOut.String())
		}
	}
}
