package agent

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// TestSentinela (docs/monitoramento.md, seção 3): a primeira leitura é a base; usuário, IP,
// software e desempenho adiantam o check-in, no máximo um a cada 5 minutos; o check-in feito
// leva as amostras e traz os limites.
func TestSentinela(t *testing.T) {
	agora := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	usuarios := []protocol.LoggedUser{{Account: `TJ\ana`, Session: protocol.SessionConsole, Active: true}}
	ip := "10.0.0.5/24"
	software := []protocol.Software{{Name: "7-Zip", Version: "24.08"}}
	cpu := 10.0
	veth := "172.17.0.2/16"
	a := &Agent{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		now: func() time.Time { return agora }, rand: func(n int64) int64 { return 0 },
		Watch: &Watch{
			State: func(context.Context) protocol.MachineState {
				return protocol.MachineState{Users: usuarios, Interfaces: []protocol.NetInterface{
					{Name: "eth0", Up: true, IPv4: []string{ip}}, {Name: "veth1", Up: true, IPv4: []string{veth}}},
					Disks: []protocol.Disk{{Mount: "C:", TotalBytes: 100, FreeBytes: 50}}}
			},
			Sample: func(_ context.Context, at time.Time, d []protocol.Disk) (protocol.PerfSample, error) {
				return protocol.PerfSample{At: at, CPU: cpu, Mem: 20, Disks: map[string]float64{"C:": 50}}, nil
			},
			Software:      func(context.Context) ([]protocol.Software, error) { return software, nil },
			SoftwareEvery: 15 * time.Minute,
		}}
	acordou := func() string {
		select {
		case <-a.wakeChan():
			r, _ := a.wakeReason.Load().(string)
			return r
		default:
			return ""
		}
	}
	minuto := func() string {
		a.watchOnceTick(context.Background())
		agora = agora.Add(time.Minute)
		return acordou()
	}
	if r := minuto(); r != "" {
		t.Fatalf("a base não pode adiantar: %s", r)
	}
	usuarios = append(usuarios, protocol.LoggedUser{Account: `TJ\bruno`, Session: protocol.SessionRDP})
	if r := minuto(); r != ReasonUser {
		t.Fatalf("usuário: %q", r)
	}
	veth = "172.17.0.3/16" // interface de container: não conta
	if r := minuto(); r != "" {
		t.Fatalf("veth: %q", r)
	}
	ip = "10.0.0.9/24"
	if r := minuto(); r != "" {
		t.Fatalf("antes de 5 min do anterior: %q", r)
	}
	for i := 0; i < 2; i++ {
		if r := minuto(); r != "" {
			t.Fatalf("antes de 5 min do anterior: %q", r)
		}
	}
	if r := minuto(); r != ReasonNetwork {
		t.Fatalf("rede, 5 min depois: %q", r)
	}
	// O check-in feito leva as amostras e os limites novos.
	if n := len(a.perfBatch()); n != 7 {
		t.Fatalf("amostras: %d", n)
	}
	a.perfSent(7, &protocol.MonitorPolicy{CPUPct: 80, MemPct: 90, DiskFreePct: 10, Minutes: 2})
	if len(a.perfBatch()) != 0 || a.ws.policy.CPUPct != 80 {
		t.Fatal("perfSent")
	}
	// Desempenho: 2 minutos acima de 80% (o limite novo).
	agora = agora.Add(10 * time.Minute)
	cpu = 85
	minuto()
	if r := minuto(); r != ReasonPerf {
		t.Fatalf("desempenho: %q", r)
	}
	// Software: só na conferência de 15 em 15 minutos.
	software = append(software, protocol.Software{Name: "Firefox", Version: "140"})
	cpu = 10
	agora = agora.Add(20 * time.Minute)
	if r := minuto(); r != ReasonSoftware {
		t.Fatalf("software: %q", r)
	}
	// Fila cheia: as mais velhas saem.
	for range protocol.MaxPerfSamples + 5 {
		a.watchOnceTick(context.Background())
		agora = agora.Add(time.Minute)
	}
	if n := len(a.perfBatch()); n != protocol.MaxPerfSamples {
		t.Fatalf("fila: %d", n)
	}
}
