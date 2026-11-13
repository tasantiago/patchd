package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// contado conta as perguntas ao catálogo que valem para o parque inteiro.
type contado struct {
	*bancoFrota
	produtos, fixes, refs, situacao atomic.Int64
}

func (c *contado) WindowsProducts(ctx context.Context) ([]compliance.MSRCProduct, error) {
	c.produtos.Add(1)
	return c.bancoFrota.WindowsProducts(ctx)
}

func (c *contado) WindowsFixes(ctx context.Context, ids []string) ([]compliance.WindowsFix, error) {
	c.fixes.Add(1)
	return c.bancoFrota.WindowsFixes(ctx, ids)
}

func (c *contado) ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error) {
	c.refs.Add(1)
	return c.bancoFrota.ThirdPartyVersions(ctx)
}

func (c *contado) CatalogStatus(ctx context.Context, sources []string) ([]store.CatalogSourceStatus, error) {
	c.situacao.Add(1)
	return c.bancoFrota.CatalogStatus(ctx, sources)
}

// Com muitas máquinas, o catálogo é consultado uma vez por passada (não uma vez por máquina),
// as máquinas com o mesmo produto e o mesmo build apontam para a MESMA avaliação do Windows,
// e a lista de programas sem regra não fica guardada.
func TestAvaliacaoDoParqueCompartilhaOCatalogo(t *testing.T) {
	agora := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	const n = 60
	b := &bancoFrota{bancoComplianceFalso: &bancoComplianceFalso{inv: map[string]protocol.InventoryReport{},
		produtos: []compliance.MSRCProduct{{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"}},
		windows: []compliance.WindowsFix{
			{Document: "2026-Oct", Released: agora.Add(-30 * 24 * time.Hour), CVE: "CVE-2026-0001", Title: "Kernel", Severity: "Important", Score: 7.8,
				KB: "5060001", SubType: "Security Update", Build: "10.0.26200.7000"},
			{Document: "2026-Nov", Released: agora.Add(-3 * 24 * time.Hour), CVE: "CVE-2026-0002", Title: "Win32k", Severity: "Critical", Score: 8.8,
				KB: "5060002", SubType: "Security Update", Build: "10.0.26200.7100"},
		}}}
	for i := range n {
		id := fmt.Sprintf("win-%03d", i)
		build := "26200.6900" // falta tudo
		if i%2 == 1 {
			build = "26200.7100" // em dia
		}
		b.inv[id] = protocol.InventoryReport{SchemaVersion: 1, CollectedAt: agora, Hash: "h-" + id,
			OS: protocol.OSInfo{ID: "windows", Family: "windows", Name: "Windows 11 Pro", Version: "25H2", Build: build, Arch: "amd64", Hostname: id},
			Software: []protocol.Software{{Name: "Programa Sem Regra " + id, Version: "1.0", Scope: "machine", Source: "registry"},
				{Name: "7-Zip 19.00 (x64)", Version: "19.00", Publisher: "Igor Pavlov", Scope: "machine", Source: "registry"}}}
		b.maquinas = append(b.maquinas, protocol.MachineSummary{ID: id, Hostname: id, OSName: "Windows 11 Pro", OSVersion: "25H2",
			LastSeenAt: agora, InventoryHash: "h-" + id})
	}
	st := &contado{bancoFrota: b}
	manifest, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	s := newComplianceService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return agora }
	s.manifest = &manifest
	fleet, err := s.fleet(context.Background(), modeWarm)
	if err != nil || len(fleet.Machines) != n {
		t.Fatalf("parque: %d máquinas, %v", len(fleet.Machines), err)
	}
	if fleet.Counts["faltando"] != n/2 || fleet.Counts["em_dia"] != n/2 {
		t.Fatalf("estados: %v", fleet.Counts)
	}
	if p, f, r := st.produtos.Load(), st.fixes.Load(), st.refs.Load(); p != 1 || f != 1 || r != 1 {
		t.Errorf("consultas ao catálogo para %d máquinas: produtos=%d correções=%d referências=%d, esperava 1 de cada", n, p, f, r)
	}
	// A situação das fontes: uma para a chave do catálogo e uma para a avaliação.
	if c := st.situacao.Load(); c > 3 {
		t.Errorf("situação das fontes consultada %d vezes", c)
	}
	distintos := map[*compliance.WindowsResult]int{}
	s.cache.mu.Lock()
	for id, e := range s.cache.entries {
		if e.ev.Windows == nil {
			t.Fatalf("%s sem avaliação do Windows: %+v", id, e.ev)
		}
		distintos[e.ev.Windows]++
		if e.ev.Third == nil || e.ev.Third.Unmatched != nil || e.ev.Third.Programs != 2 {
			t.Errorf("%s: os terceiros ficam sem a lista dos sem regra, com a contagem: %+v", id, e.ev.Third)
		}
	}
	s.cache.mu.Unlock()
	if len(distintos) != 2 {
		t.Errorf("avaliações distintas do Windows para 2 builds: %d", len(distintos))
	}

	// A lista por item sai compartilhada, e o conteúdo é o de sempre.
	items, err := s.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var com, primeiro int
	for i, mi := range items {
		if len(mi.Items) == 0 {
			continue
		}
		if com == 0 {
			primeiro = i
		}
		com++
		if len(mi.Items) != 2 || mi.Items[0].Installed != "26200.6900" || &mi.Items[0] != &items[primeiro].Items[0] {
			t.Fatalf("itens de %s: %+v", mi.MachineID, mi.Items)
		}
	}
	if com != n/2 {
		t.Errorf("máquinas com pendência: %d", com)
	}

	// Catálogo novo (outra chave): memo novo, as consultas se repetem uma vez.
	b.situacao = map[string]store.CatalogSourceStatus{"msrc": {Source: "msrc", LastOK: agora, Runs: 9}}
	if _, err := s.fleet(context.Background(), modeWarm); err != nil {
		t.Fatal(err)
	}
	if f := st.fixes.Load(); f != 2 {
		t.Errorf("depois de o catálogo mudar, as correções são lidas de novo, uma vez: %d", f)
	}
}

// O memo calcula uma vez por chave mesmo com vários pedindo juntos, e não guarda erro.
func TestMemoCalculaUmaVez(t *testing.T) {
	m := newEvalMemo("k", time.Now())
	var calls atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := memoized(context.Background(), m, "a", func() (int, error) {
				calls.Add(1)
				time.Sleep(5 * time.Millisecond)
				return 42, nil
			})
			if v != 42 || err != nil {
				t.Errorf("valor: %d %v", v, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("calculou %d vezes", calls.Load())
	}
	falha := errors.New("banco fora")
	if _, err := memoized(context.Background(), m, "b", func() (int, error) { return 0, falha }); !errors.Is(err, falha) {
		t.Fatalf("erro: %v", err)
	}
	if v, err := memoized(context.Background(), m, "b", func() (int, error) { return 7, nil }); v != 7 || err != nil {
		t.Errorf("depois do erro, tenta de novo: %d %v", v, err)
	}
	// Quem espera desiste com o próprio contexto.
	preso := make(chan struct{})
	go func() { _, _ = memoized(context.Background(), m, "c", func() (int, error) { <-preso; return 1, nil }) }()
	time.Sleep(5 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := memoized(ctx, m, "c", func() (int, error) { return 2, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("espera com prazo: %v", err)
	}
	close(preso)

	// O serviço troca de memo quando o catálogo muda ou o prazo vence.
	s := &complianceService{}
	t0 := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	a := s.memoFor("c1", t0)
	if s.memoFor("c1", t0.Add(time.Minute)) != a {
		t.Error("mesmo catálogo, dentro do prazo: o mesmo memo")
	}
	if s.memoFor("c2", t0.Add(time.Minute)) == a {
		t.Error("catálogo novo: memo novo")
	}
	c := s.memoFor("c2", t0.Add(time.Minute))
	if s.memoFor("c2", t0.Add(time.Minute+memoTTL+time.Second)) == c {
		t.Error("prazo vencido: memo novo")
	}
}
