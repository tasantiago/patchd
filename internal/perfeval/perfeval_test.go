package perfeval

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestRegra(t *testing.T) {
	p := protocol.MonitorPolicy{CPUPct: 90, MemPct: 90, DiskFreePct: 10, Minutes: 3}
	st := State{}
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var mudou []Change
	passo := func(min int, cpu, disk float64) {
		mudou = append(mudou, st.Step(p, protocol.PerfSample{At: t0.Add(time.Duration(min) * time.Minute), CPU: cpu, Mem: 10,
			Disks: map[string]float64{"C:": disk}})...)
	}
	passo(0, 95, 50)
	passo(1, 99, 50)
	if len(mudou) != 0 {
		t.Fatalf("abriu cedo: %+v", mudou)
	}
	passo(2, 97, 50) // terceira seguida: abre
	if len(mudou) != 1 || !mudou[0].Opened || mudou[0].Key.Metric != MetricCPU || mudou[0].Peak != 97 {
		t.Fatalf("abrir: %+v", mudou)
	}
	passo(3, 50, 50) // uma dentro, e volta: não fecha
	passo(4, 98, 50)
	passo(5, 40, 50)
	passo(6, 40, 50)
	if len(mudou) != 1 || st["cpu|"].Peak != 98 {
		t.Fatalf("fechou cedo: %+v %+v", mudou, st["cpu|"])
	}
	passo(7, 40, 5) // terceira dentro: fecha; disco começa
	if len(mudou) != 2 || mudou[1].Opened || mudou[1].Peak != 98 {
		t.Fatalf("fechar: %+v", mudou)
	}
	// Buraco (máquina desligada): a contagem do disco recomeça.
	passo(8, 10, 5)
	passo(30, 10, 5)
	passo(31, 10, 5)
	if len(mudou) != 2 {
		t.Fatalf("o buraco não zerou: %+v", mudou)
	}
	passo(32, 10, 4)
	if len(mudou) != 3 || mudou[2].Key != (Key{MetricDisk, "C:"}) || !mudou[2].Opened || mudou[2].Value != 4 {
		t.Fatalf("disco: %+v", mudou)
	}
	if KeyOf("disk|C:") != (Key{MetricDisk, "C:"}) || KeyOf("cpu|") != (Key{Metric: MetricCPU}) {
		t.Error("KeyOf")
	}
}
