package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
)

// lento atrasa a lista de ecossistemas do Ubuntu, como um DISTINCT numa tabela grande.
type lento struct{ *bancoFrota }

func (l lento) UbuntuEcosystemsKnown(ctx context.Context) ([]string, error) {
	time.Sleep(30 * time.Millisecond)
	return l.bancoFrota.UbuntuEcosystemsKnown(ctx)
}

func TestTemposDaAvaliacao(t *testing.T) {
	agora := time.Now().UTC()
	b := &bancoFrota{bancoComplianceFalso: &bancoComplianceFalso{inv: map[string]protocol.InventoryReport{
		"ubuntu-1": {SchemaVersion: 1, CollectedAt: agora, OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04"},
			Software: []protocol.Software{{Name: "curl", Version: "1", SourcePackage: "curl", SourcePackageVersion: "1"}}},
	}}, maquinas: []protocol.MachineSummary{{ID: "ubuntu-1", LastSeenAt: agora}}}
	times := newStepTimes()
	st := timedStore{st: lento{b}, t: times}
	log := &bytes.Buffer{}
	s := &complianceService{st: st, logger: slog.New(slog.NewTextHandler(log, nil)), now: func() time.Time { return agora }}

	inicio := time.Now()
	if _, err := s.Fleet(context.Background()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	times.print(&out, time.Since(inicio))
	o := out.String()
	linhas := strings.Split(o, "\n")
	// A mais lenta vem primeiro, logo depois do cabeçalho.
	if len(linhas) < 4 || !strings.Contains(linhas[3], "UbuntuEcosystemsKnown") || !strings.Contains(o, "fora do banco (Go)") {
		t.Errorf("tempos:\n%s", o)
	}
	for _, consulta := range []string{"Machines", "LatestInventory", "UbuntuAdvisories", "CatalogStatus"} {
		if !strings.Contains(o, consulta) {
			t.Errorf("faltou %s:\n%s", consulta, o)
		}
	}
	if times.total["UbuntuEcosystemsKnown"] < 30*time.Millisecond || times.calls["LatestInventory"] != 1 {
		t.Errorf("contagem: %v %v", times.total, times.calls)
	}
}

func TestAvaliacaoLentaEInterrompida(t *testing.T) {
	agora := time.Now().UTC()
	b := &bancoFrota{bancoComplianceFalso: &bancoComplianceFalso{inv: map[string]protocol.InventoryReport{
		"ubuntu-1": {SchemaVersion: 1, CollectedAt: agora, OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04"}},
	}}, maquinas: []protocol.MachineSummary{{ID: "ubuntu-1", LastSeenAt: agora}}}
	log := &bytes.Buffer{}
	s := &complianceService{st: lentoDemais{b}, logger: slog.New(slog.NewTextHandler(log, nil)), now: func() time.Time { return agora }}
	if _, err := s.Fleet(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "avaliação de compliance lenta") || !strings.Contains(log.String(), "-tempos") {
		t.Errorf("a avaliação lenta vai para o log com a dica:\n%s", log.String())
	}

	// Cliente que desiste: nada de ERROR no log; a frota devolve context.Canceled.
	log.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&complianceService{st: b, logger: slog.New(slog.NewTextHandler(log, nil)), now: func() time.Time { return agora }}).Fleet(ctx)
	if err == nil || strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("cancelado: %v\n%s", err, log.String())
	}
	cs := (&complianceService{st: canceladoNoMeio{b}, logger: slog.New(slog.NewTextHandler(log, nil)), now: func() time.Time { return agora }}).
		status(context.Background(), b.maquinas[0], agora)
	if cs.State != string(compliance.StateUnknown) || strings.Contains(log.String(), "level=ERROR") {
		t.Errorf("interrompida no meio: %+v\n%s", cs, log.String())
	}
}

type lentoDemais struct{ *bancoFrota }

func (l lentoDemais) UbuntuEcosystemsKnown(ctx context.Context) ([]string, error) {
	time.Sleep(slowEvaluation + 50*time.Millisecond)
	return l.bancoFrota.UbuntuEcosystemsKnown(ctx)
}

type canceladoNoMeio struct{ *bancoFrota }

func (c canceladoNoMeio) UbuntuEcosystemsKnown(context.Context) ([]string, error) {
	return nil, context.Canceled
}
