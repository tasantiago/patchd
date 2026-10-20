package api

import (
	"testing"
	"time"
)

func TestLimitePorMaquina(t *testing.T) {
	var l agentLimiter
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for i := range 10 {
		if ok, _ := l.allow("m1", limitReports, t0); !ok {
			t.Fatalf("relatório %d recusado dentro da rajada", i+1)
		}
	}
	ok, wait := l.allow("m1", limitReports, t0)
	if ok || wait != 3*time.Minute {
		t.Fatalf("11º relatório: ok=%v espera=%v", ok, wait)
	}
	// Outra máquina e a outra classe não são afetadas.
	if ok, _ := l.allow("m2", limitReports, t0); !ok {
		t.Error("a outra máquina pagou pela primeira")
	}
	if ok, _ := l.allow("m1", limitGeneral, t0); !ok {
		t.Error("o check-in pagou pelos relatórios")
	}
	// Um minuto depois ainda falta; três minutos depois, uma ficha.
	if ok, wait := l.allow("m1", limitReports, t0.Add(time.Minute)); ok || wait != 2*time.Minute {
		t.Errorf("1 min depois: ok=%v espera=%v", ok, wait)
	}
	if ok, _ := l.allow("m1", limitReports, t0.Add(3*time.Minute)); !ok {
		t.Error("3 min depois: sem ficha")
	}
	// O balde não passa da rajada, por mais que a máquina fique quieta.
	t1 := t0.Add(24 * time.Hour)
	n := 0
	for {
		if ok, _ := l.allow("m3", limitGeneral, t1); !ok {
			break
		}
		n++
	}
	if n != 60 {
		t.Errorf("rajada do geral: %d, esperava 60", n)
	}
	// A limpeza esquece os baldes cheios, e o aviso no log sai um a cada 10 minutos.
	l.allow("m4", limitGeneral, t1.Add(time.Hour))
	if _, ok := l.buckets["geral|m2"]; ok {
		t.Error("balde cheio não foi esquecido")
	}
	if _, ok := l.buckets["geral|m3"]; ok {
		t.Error("balde reposto não foi esquecido")
	}
	if !l.warn("m1", t1) || l.warn("m1", t1.Add(9*time.Minute)) || !l.warn("m1", t1.Add(10*time.Minute)) {
		t.Error("aviso no log: um a cada 10 minutos")
	}
}
