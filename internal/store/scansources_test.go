package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

type contratoFontes interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (bool, error)
	SaveScan(ctx context.Context, machineID string, rep protocol.PatchScanReport) error
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	SourceFailures(ctx context.Context, machineID string) ([]SourceFailure, error)
}

// Aula 16.2: a fonte que falha na busca atual, com a última busca em que ela respondeu.
func testarFontes(t *testing.T, st contratoFontes) {
	ctx := context.Background()
	dia := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
	busca := func(maq string, em time.Time, wsusOK bool) {
		t.Helper()
		def := protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{}}
		if !wsusOK {
			def = protocol.ScanResult{Source: "wua-default", Error: "busca no WUA: Search: HRESULT 0x80244007"}
		}
		rep := protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: em,
			Scans: []protocol.ScanResult{def, {Source: "wua-offline", Missing: []protocol.MissingUpdate{}}}}
		if err := st.SaveScan(ctx, maq, rep); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = st.SaveInventory(ctx, "maq-a", inv("aaaa", "24.09"))
	busca("maq-a", dia, true)
	busca("maq-a", dia.Add(24*time.Hour), false)
	busca("maq-a", dia.Add(48*time.Hour), false)
	busca("maq-b", dia, false) // nunca respondeu
	busca("maq-c", dia, true)  // em dia
	busca("maq-d", dia, false) // aposentada: fora
	_, _ = st.RetireMachine(ctx, "maq-d", "teste", "teste")

	l, err := st.SourceFailures(ctx, "")
	if err != nil || len(l) != 2 {
		t.Fatalf("falhas: %+v %v", l, err)
	}
	a, b := l[0], l[1]
	if a.MachineID != "maq-a" || a.Hostname != "pc-lab-01" || a.Source != "wua-default" || a.Error != "busca no WUA: Search: HRESULT 0x80244007" ||
		!a.ScannedAt.Equal(dia.Add(48*time.Hour)) || a.LastOK == nil || !a.LastOK.Equal(dia) {
		t.Errorf("maq-a: %+v", a)
	}
	if b.MachineID != "maq-b" || b.LastOK != nil {
		t.Errorf("maq-b nunca respondeu: %+v", b)
	}
	if l, _ := st.SourceFailures(ctx, "maq-b"); len(l) != 1 || l[0].MachineID != "maq-b" {
		t.Errorf("só uma máquina: %+v", l)
	}
	busca("maq-a", dia.Add(72*time.Hour), true)
	if l, _ := st.SourceFailures(ctx, "maq-a"); len(l) != 0 {
		t.Errorf("voltou a responder: %+v", l)
	}
}

func TestFontesMemoria(t *testing.T) { testarFontes(t, NewMemory()) }

func TestFontesPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarFontes(t, NewPostgres(pool))
}
