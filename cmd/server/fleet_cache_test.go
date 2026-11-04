package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// contaLeituras conta os inventários lidos: cada um é uma avaliação feita de novo.
type contaLeituras struct {
	*bancoFrota
	leituras atomic.Int32
}

func (c *contaLeituras) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	c.leituras.Add(1)
	return c.bancoFrota.LatestInventory(ctx, id)
}

// O cache das avaliações: a segunda página não reavalia; inventário novo, busca nova,
// catálogo sincronizado e o prazo refazem só o que mudou; "sem contato" não espera o cache.
func TestCacheDasAvaliacoes(t *testing.T) {
	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	inv := protocol.InventoryReport{SchemaVersion: 1, CollectedAt: agora, OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04"}}
	b := &contaLeituras{bancoFrota: &bancoFrota{bancoComplianceFalso: &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{"m1": inv, "m2": inv, "m3": inv}, situacao: map[string]store.CatalogSourceStatus{}}}}
	for _, id := range []string{"m1", "m2", "m3"} {
		b.maquinas = append(b.maquinas, protocol.MachineSummary{ID: id, LastSeenAt: agora, InventoryHash: "h-" + id})
	}
	relogio := agora
	s := newComplianceService(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return relogio }
	ctx := context.Background()
	fleet := func() protocol.ComplianceFleet {
		t.Helper()
		f, err := s.Fleet(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	leu := func(quer int32, oque string) {
		t.Helper()
		if n := b.leituras.Swap(0); n != quer {
			t.Errorf("%s: %d avaliações, quero %d", oque, n, quer)
		}
	}
	fleet()
	leu(3, "primeira página")
	fleet()
	if _, err := s.Items(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Detail(ctx, "m2"); err != nil {
		t.Fatal(err)
	}
	leu(0, "segunda página, pendências e detalhe")

	b.maquinas[0].InventoryHash = "h-m1-novo"
	fleet()
	leu(1, "inventário novo")
	busca := agora
	b.maquinas[1].ScanReceivedAt = &busca
	fleet()
	leu(1, "busca nova")
	b.situacao["ubuntu"] = store.CatalogSourceStatus{Source: "ubuntu", Runs: 1}
	fleet()
	leu(3, "catálogo sincronizado")
	relogio = relogio.Add(evalTTL)
	fleet()
	leu(3, "prazo")

	// O contato velho vale na hora, sem reavaliar.
	b.maquinas[2].LastSeenAt = relogio.Add(-30 * 24 * time.Hour)
	f := fleet()
	leu(0, "só o último contato mudou")
	if f.Counts["sem_dado_recente"] != 1 {
		t.Errorf("sem contato não contou: %+v", f.Counts)
	}
	// Máquina que sai do parque sai do cache.
	b.maquinas = b.maquinas[:2]
	fleet()
	if _, ok := s.cache.entries["m3"]; ok {
		t.Error("a aposentada ficou no cache")
	}
}
