package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/metrics"
	"github.com/tasantiago/patchd/internal/store"
)

type pruneFalso struct {
	chamadas chan store.Retention
	dry      []bool
}

func (p *pruneFalso) Prune(ctx context.Context, r store.Retention, now time.Time, dry bool) (store.PruneResult, error) {
	p.dry = append(p.dry, dry)
	if p.chamadas != nil {
		p.chamadas <- r
	}
	return store.PruneResult{Inventories: 5, Scans: 3}, nil
}

func (p *pruneFalso) TableSizes(ctx context.Context) (map[string]int64, int64, error) {
	return map[string]int64{"inventory_reports": 300 << 20, "scan_reports": 40 << 20, "jobs": 2 << 20}, 400 << 20, nil
}

func TestRetentionLoopRodaNaPartidaEParaNoEncerramento(t *testing.T) {
	p := &pruneFalso{chamadas: make(chan store.Retention, 1)}
	reg := metrics.NewRegistry()
	apagados := reg.NewCounter("patchd_retencao_apagados_total", "x", "tabela")
	r := store.Retention{History: 90 * 24 * time.Hour, Jobs: 400 * 24 * time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	fim := make(chan struct{})
	go func() {
		retentionLoop(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), p, func() store.Retention { return r }, apagados)
		close(fim)
	}()
	select {
	case got := <-p.chamadas:
		if got != r {
			t.Errorf("cortes: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a retenção deveria rodar na partida")
	}
	cancel()
	select {
	case <-fim:
	case <-time.After(2 * time.Second):
		t.Fatal("o laço deveria parar no encerramento")
	}
	if p.dry[0] || apagados.Value("inventarios") != 5 || apagados.Value("buscas") != 3 {
		t.Errorf("dry=%v contadores: %v %v", p.dry, apagados.Value("inventarios"), apagados.Value("buscas"))
	}
}

func TestConfiguracaoDaRetencao(t *testing.T) {
	r, err := retentionConfig(lookMap(nil))
	if err != nil || r.History != 90*24*time.Hour || r.Jobs != 400*24*time.Hour {
		t.Errorf("padrão: %+v %v", r, err)
	}
	r, err = retentionConfig(lookMap(map[string]string{"PATCHD_RETENTION_JOBS_DAYS": "0", "PATCHD_RETENTION_HISTORY_DAYS": "180"}))
	if err != nil || r.Jobs != 0 || r.History != 180*24*time.Hour || !strings.Contains(describeRetention(r), "guardados sempre") {
		t.Errorf("jobs sempre: %+v %v", r, err)
	}
	for k, v := range map[string]string{
		"PATCHD_RETENTION_HISTORY_DAYS": "0", // histórico sem limite não: a tabela cresce sem fim
		"PATCHD_RETENTION_JOBS_DAYS":    "30",
	} {
		if _, err := retentionConfig(lookMap(map[string]string{k: v})); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	if _, err := retentionConfig(lookMap(map[string]string{"PATCHD_RETENTION_HISTORY_DAYS": "90d"})); err == nil {
		t.Error("90d aceito")
	}
	// Aula 18.1: a trilha de auditoria — 5 anos por padrão, 0 guarda sempre, menos de 1 ano não.
	if r, _ := retentionConfig(lookMap(nil)); r.Audit != 1825*24*time.Hour || !strings.Contains(describeRetention(r), "auditoria: 1825 dias") {
		t.Errorf("auditoria padrão: %+v", r)
	}
	if r, err := retentionConfig(lookMap(map[string]string{"PATCHD_RETENTION_AUDIT_DAYS": "0"})); err != nil || r.Audit != 0 || !strings.Contains(describeRetention(r), "auditoria guardada sempre") {
		t.Errorf("auditoria sempre: %+v %v", r, err)
	}
	if _, err := retentionConfig(lookMap(map[string]string{"PATCHD_RETENTION_AUDIT_DAYS": "90"})); err == nil || !strings.Contains(err.Error(), "PATCHD_RETENTION_AUDIT_DAYS") {
		t.Errorf("auditoria com 90 dias: %v", err)
	}
}

func TestComandoRetention(t *testing.T) {
	p := &pruneFalso{}
	r := store.Retention{History: 90 * 24 * time.Hour}
	var out, errb bytes.Buffer
	if code := retentionCommand(context.Background(), p, r, nil, time.Now(), &out, &errb); code != exitOK {
		t.Fatalf("saída %d: %s", code, errb.String())
	}
	for _, s := range []string{"simulação: nada foi apagado", "inventarios  5", "buscas       3", "jobs guardados sempre",
		"banco: 400.0 MiB", "inventory_reports  300.0 MiB"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("falta %q em:\n%s", s, out.String())
		}
	}
	out.Reset()
	if code := retentionCommand(context.Background(), p, r, []string{"-apply"}, time.Now(), &out, &errb); code != exitOK ||
		!strings.Contains(out.String(), "linhas apagadas:") || !strings.Contains(out.String(), "autovacuum") {
		t.Errorf("apply: %d\n%s", code, out.String())
	}
	if len(p.dry) != 2 || !p.dry[0] || p.dry[1] {
		t.Errorf("simulação e depois de verdade: %v", p.dry)
	}
	if code := retentionCommand(context.Background(), p, r, []string{"lixo"}, time.Now(), &out, &errb); code != exitConfig {
		t.Errorf("argumento sobrando: %d", code)
	}
}
