package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// contrato é o que a API exige de um armazenamento (o mesmo conjunto de api.Store).
type contrato interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (bool, error)
	LatestInventory(ctx context.Context, machineID string) (protocol.InventoryReport, bool, error)
	SaveScan(ctx context.Context, machineID string, rep protocol.PatchScanReport) error
	LatestScan(ctx context.Context, machineID string) (protocol.PatchScanReport, bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
}

func inv(hash, versao string) protocol.InventoryReport {
	return protocol.InventoryReport{
		SchemaVersion: protocol.InventorySchemaVersion,
		AgentVersion:  "v0.4.0-teste",
		CollectedAt:   time.Date(2026, 10, 4, 1, 25, 17, 0, time.UTC),
		Hash:          "sha256:" + hash,
		OS:            protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Version: "26H2", Hostname: "FJ106260"},
		Software:      []protocol.Software{{Name: "7-Zip", Version: versao, Scope: "machine", Source: "uninstall"}},
	}
}

// testarContrato roda as mesmas verificações em qualquer armazenamento: a memória e o
// PostgreSQL precisam se comportar igual para a API.
func testarContrato(t *testing.T, st contrato) {
	ctx := context.Background()

	if _, found, err := st.LatestInventory(ctx, "nunca-vista"); found || err != nil {
		t.Fatalf("máquina desconhecida: found=%t err=%v", found, err)
	}

	gravou, err := st.SaveInventory(ctx, "estacao-teste", inv("aaaa", "24.09"))
	if err != nil || !gravou {
		t.Fatalf("primeiro inventário deveria ser gravado: %t %v", gravou, err)
	}
	time.Sleep(10 * time.Millisecond)
	gravou, err = st.SaveInventory(ctx, "estacao-teste", inv("aaaa", "24.09"))
	if err != nil || gravou {
		t.Fatalf("mesmo hash não deveria gravar: %t %v", gravou, err)
	}

	lista, err := st.Machines(ctx)
	if err != nil || len(lista) != 1 {
		t.Fatalf("lista: %+v %v", lista, err)
	}
	m := lista[0]
	if m.InventoryChangedAt == nil || !m.LastSeenAt.After(*m.InventoryChangedAt) {
		t.Errorf("o reenvio sem mudança atualiza o último contato, não a data da mudança: %+v", m)
	}

	if gravou, _ := st.SaveInventory(ctx, "estacao-teste", inv("bbbb", "25.01")); !gravou {
		t.Fatal("hash novo deveria gravar")
	}
	rep, found, err := st.LatestInventory(ctx, "estacao-teste")
	if err != nil || !found || rep.Hash != "sha256:bbbb" || rep.Software[0].Version != "25.01" || !rep.CollectedAt.Equal(inv("", "").CollectedAt) {
		t.Errorf("deveria devolver o inventário mais novo, inteiro: %+v %t %v", rep, found, err)
	}

	sim := true
	scan := protocol.PatchScanReport{
		SchemaVersion: protocol.PatchSchemaVersion,
		ScannedAt:     time.Date(2026, 10, 4, 1, 29, 24, 0, time.UTC),
		Scans:         []protocol.ScanResult{{Source: "wua-default"}},
		Reboot:        &protocol.RebootStatus{Pending: &sim},
	}
	if err := st.SaveScan(ctx, "estacao-teste", scan); err != nil {
		t.Fatal(err)
	}
	// Uma máquina só com busca, sem inventário, e um ID que testa a ordenação byte a byte.
	semReboot := protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, Reboot: &protocol.RebootStatus{}}
	if err := st.SaveScan(ctx, "Z-maiuscula", semReboot); err != nil {
		t.Fatal(err)
	}

	got, found, err := st.LatestScan(ctx, "estacao-teste")
	if err != nil || !found || got.Reboot == nil || got.Reboot.Pending == nil || !*got.Reboot.Pending || got.Scans[0].Source != "wua-default" {
		t.Errorf("busca devolvida errada: %+v %t %v", got, found, err)
	}

	lista, _ = st.Machines(ctx)
	if len(lista) != 2 || lista[0].ID != "Z-maiuscula" || lista[1].ID != "estacao-teste" {
		t.Fatalf("ordem byte a byte (maiúsculas antes): %+v", lista)
	}
	z, e := lista[0], lista[1]
	if z.Hostname != "" || z.InventoryChangedAt != nil || z.ScanReceivedAt == nil || z.RebootPending != nil {
		t.Errorf("máquina só com busca e reinício desconhecido: %+v", z)
	}
	if e.Hostname != "FJ106260" || e.OSName != "Windows 11 Pro" || e.InventoryHash != "sha256:bbbb" ||
		e.RebootPending == nil || !*e.RebootPending || e.LastSeenAt.Location() != time.UTC {
		t.Errorf("resumo da estação errado: %+v", e)
	}
}

func TestContratoMemoria(t *testing.T) {
	testarContrato(t, NewMemory())
}
