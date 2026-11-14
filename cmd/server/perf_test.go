package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A passada de hora em hora monta as horas fechadas e apaga as amostras de mais de 7 dias.
func TestPassadaDoDesempenho(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	if _, err := st.SaveInventory(ctx, "m-1", protocol.InventoryReport{SchemaVersion: 1, Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	agora := time.Date(2026, 10, 30, 12, 30, 0, 0, time.UTC)
	velha := protocol.PerfSample{At: agora.Add(-8 * 24 * time.Hour), CPU: 1, Mem: 1}
	hora := protocol.PerfSample{At: agora.Add(-time.Hour), CPU: 40, Mem: 50, Disks: map[string]float64{"/": 20}}
	atual := protocol.PerfSample{At: agora.Add(-time.Minute), CPU: 60, Mem: 50}
	if _, err := st.SavePerf(ctx, "m-1", []protocol.PerfSample{velha, hora, atual}, protocol.DefaultMonitorPolicy, agora); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	estado := &perfState{}
	perfPass(ctx, log, st, agora, estado)
	if !estado.rolled || !estado.hoursPruned.Equal(agora) {
		t.Fatalf("a passada não montou as horas ou não limpou: %+v", estado)
	}
	hs, _ := st.PerfHourly(ctx, "m-1", agora.Add(-48*time.Hour))
	if len(hs) != 1 || !hs[0].Hour.Equal(agora.Add(-time.Hour).Truncate(time.Hour)) || hs[0].CPUAvg != 40 {
		t.Fatalf("horas (só a fechada): %+v", hs)
	}
	// Depois da primeira, a passada só refaz as horas recentes; a amostra que chega atrasada
	// (o notebook que voltou para a rede) marca a hora dela, e a passada seguinte a monta.
	atrasada := protocol.PerfSample{At: agora.Add(-20 * time.Hour), CPU: 80, Mem: 10}
	recente := protocol.PerfSample{At: agora.Add(-time.Hour).Add(time.Minute), CPU: 20, Mem: 50}
	if _, err := st.SavePerf(ctx, "m-1", []protocol.PerfSample{atrasada, recente}, protocol.DefaultMonitorPolicy, agora); err != nil {
		t.Fatal(err)
	}
	perfPass(ctx, log, st, agora.Add(10*time.Minute), estado)
	if !estado.hoursPruned.Equal(agora) {
		t.Fatalf("as horas são limpas uma vez por dia, não a cada passada: %+v", estado)
	}
	hs, _ = st.PerfHourly(ctx, "m-1", agora.Add(-48*time.Hour))
	if len(hs) != 2 || !hs[0].Hour.Equal(atrasada.At.Truncate(time.Hour)) || hs[0].CPUAvg != 80 || hs[1].CPUAvg != 30 || hs[1].Samples != 2 {
		t.Fatalf("a hora atrasada e a recente refeita: %+v", hs)
	}
	ss, _ := st.PerfSamples(ctx, "m-1", agora.Add(-30*24*time.Hour))
	if len(ss) != 4 || ss[0].At.Before(agora.Add(-perfSamplesKeep)) {
		t.Fatalf("amostras depois da retenção (a de 8 dias sai): %+v", ss)
	}
}

// janelas anota as janelas que a passada pediu para montar.
type janelas struct {
	*store.Memory
	ultima   time.Time
	pedidas  [][2]time.Time
	limpezas []time.Time // o corte das horas em cada limpeza (zero: pulou)
}

func (j *janelas) PerfLastHour(context.Context) (time.Time, error) { return j.ultima, nil }

func (j *janelas) RollupPerf(ctx context.Context, from, to time.Time) (int64, error) {
	j.pedidas = append(j.pedidas, [2]time.Time{from, to})
	return j.Memory.RollupPerf(ctx, from, to)
}

func (j *janelas) PrunePerf(ctx context.Context, samples, hours, alerts time.Time) (int64, error) {
	j.limpezas = append(j.limpezas, hours)
	return j.Memory.PrunePerf(ctx, samples, hours, alerts)
}

// A primeira passada depois da partida começa da última hora montada (o servidor que só
// reiniciou não refaz as 48 horas); sem hora nenhuma, ou com a parada longa, refaz as 48. As
// seguintes refazem só as horas recentes. A limpeza das horas roda uma vez por dia.
func TestJanelaDaPassadaDoDesempenho(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agora := time.Date(2026, 11, 14, 10, 20, 0, 0, time.UTC)
	hora := agora.Truncate(time.Hour)
	for nome, c := range map[string]struct {
		ultima time.Time
		desde  time.Duration
	}{
		"reinício rápido (a última hora montada é a anterior)": {hora.Add(-time.Hour), 4 * time.Hour},
		"parado por 10 horas":                {hora.Add(-10 * time.Hour), 13 * time.Hour},
		"parado por uma semana":              {hora.Add(-7 * 24 * time.Hour), perfRollupBack},
		"sem hora nenhuma (instalação nova)": {time.Time{}, perfRollupBack},
		"hora no futuro (relógio mexido)":    {hora.Add(5 * time.Hour), perfRollupBack},
	} {
		j := &janelas{Memory: store.NewMemory(), ultima: c.ultima}
		st := &perfState{}
		perfPass(ctx, log, j, agora, st)
		if len(j.pedidas) != 1 || !j.pedidas[0][1].Equal(hora) || hora.Sub(j.pedidas[0][0]) != c.desde {
			t.Errorf("%s: janela %v, esperava %v até %v", nome, j.pedidas, c.desde, hora)
		}
		// A passada seguinte: só as horas recentes, e sem limpar as horas de novo.
		perfPass(ctx, log, j, agora.Add(time.Hour), st)
		if d := j.pedidas[1][1].Sub(j.pedidas[1][0]); d != store.PerfLateAfter+time.Hour {
			t.Errorf("%s: segunda passada com janela de %v", nome, d)
		}
		if len(j.limpezas) != 2 || j.limpezas[0].IsZero() || !j.limpezas[1].IsZero() {
			t.Errorf("%s: limpeza das horas (uma por dia): %v", nome, j.limpezas)
		}
		// Um dia depois, limpa de novo.
		perfPass(ctx, log, j, agora.Add(25*time.Hour), st)
		if j.limpezas[2].IsZero() {
			t.Errorf("%s: um dia depois, as horas são limpas de novo", nome)
		}
	}
}
