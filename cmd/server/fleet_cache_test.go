package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

// Muitas vencidas de uma vez (o catálogo sincronizou): a página mostra a avaliação anterior na
// hora e acorda o Warm, que refaz. Com o preenchimento ocupado, a máquina sem avaliação nenhuma
// aparece "em avaliação" em vez de a página esperar o prazo e dar erro.
func TestPaginaNaoPagaOParqueInteiro(t *testing.T) {
	maxAntes, esperaAntes := syncRefreshMax, fillWait
	syncRefreshMax, fillWait = 1, 50*time.Millisecond
	t.Cleanup(func() { syncRefreshMax, fillWait = maxAntes, esperaAntes })

	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	inv := protocol.InventoryReport{SchemaVersion: 1, CollectedAt: agora, OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04"}}
	b := &contaLeituras{bancoFrota: &bancoFrota{bancoComplianceFalso: &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{"m1": inv, "m2": inv, "m3": inv, "m4": inv}, situacao: map[string]store.CatalogSourceStatus{}}}}
	for _, id := range []string{"m1", "m2", "m3"} {
		b.maquinas = append(b.maquinas, protocol.MachineSummary{ID: id, LastSeenAt: agora, InventoryHash: "h-" + id})
	}
	s := newComplianceService(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return agora }
	ctx := context.Background()
	// Servidor que acabou de subir: o cache vazio não vira 3 avaliações dentro da página.
	if f, err := s.Fleet(ctx); err != nil || f.Evaluating != 3 || b.leituras.Load() != 0 {
		t.Fatalf("cache vazio: err=%v %+v leituras=%d", err, f, b.leituras.Load())
	}
	<-s.kick
	if _, err := s.fleet(ctx, modeWarm); err != nil {
		t.Fatal(err)
	}
	b.leituras.Store(0)

	b.situacao["ubuntu"] = store.CatalogSourceStatus{Source: "ubuntu", Runs: 1}
	f, err := s.Fleet(ctx)
	if err != nil || b.leituras.Swap(0) != 0 || f.Refreshing != 3 || f.Counts["desconhecido"] != 0 {
		t.Fatalf("vencidas: err=%v refreshing=%d counts=%v", err, f.Refreshing, f.Counts)
	}
	select {
	case <-s.kick:
	default:
		t.Error("o Warm não foi acordado")
	}
	// O Warm refaz as três; depois a página acha tudo em dia.
	if _, err := s.fleet(ctx, modeWarm); err != nil || b.leituras.Swap(0) != 3 {
		t.Fatalf("warm: %v", err)
	}
	if f, _ := s.Fleet(ctx); f.Refreshing != 0 || f.Evaluating != 0 {
		t.Errorf("depois do warm: %+v", f)
	}

	// Preenchimento ocupado (o Warm rodando) e uma máquina nova: a página não espera.
	s.fill <- struct{}{}
	b.maquinas = append(b.maquinas, protocol.MachineSummary{ID: "m4", LastSeenAt: agora, InventoryHash: "h-m4"})
	inicio := time.Now()
	f, err = s.Fleet(ctx)
	if err != nil || f.Evaluating != 1 || time.Since(inicio) > time.Second {
		t.Fatalf("ocupado: err=%v evaluating=%d em %v", err, f.Evaluating, time.Since(inicio))
	}
	for _, m := range f.Machines {
		if m.MachineID == "m4" && (m.State != "desconhecido" || !strings.Contains(strings.Join(m.Reasons, " "), "em avaliação")) {
			t.Errorf("m4: %+v", m)
		}
	}
	<-s.fill
	if f, _ := s.Fleet(ctx); f.Evaluating != 0 || b.leituras.Load() != 1 {
		t.Errorf("livre de novo: %+v leituras=%d", f, b.leituras.Load())
	}

	// O prazo da página esgotado não vira erro (era o 500 da Visão geral).
	b.maquinas = append(b.maquinas, protocol.MachineSummary{ID: "m5", LastSeenAt: agora, InventoryHash: "h-m5"})
	dctx, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := s.Fleet(dctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("prazo: %v", err)
	}
}

// A avaliação do parque usa um quarto do pool: com o padrão (4), uma conexão só.
func TestAvaliacoesPeloTamanhoDoPool(t *testing.T) {
	for pool, quer := range map[int]int{0: 1, 4: 1, 7: 1, 8: 2, 16: 4, 100: 4} {
		if n := evalWorkersFor(pool); n != quer {
			t.Errorf("pool %d: %d, esperado %d", pool, n, quer)
		}
	}
}
