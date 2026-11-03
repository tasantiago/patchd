//go:build !linux

package srvhealth

import (
	"runtime"
	"runtime/debug"
	"time"
)

// Fora do Linux (o servidor roda em Linux; isto é para os testes e o desenvolvimento): só o
// que o runtime do Go sabe.
type otherSystem struct{}

func newSystem() system { return otherSystem{} }

func (otherSystem) info() Info {
	return Info{Cores: runtime.NumCPU(), Capacity: float64(runtime.NumCPU()), GOMAXPROCS: runtime.GOMAXPROCS(0),
		OS: runtime.GOOS, GoMemLimit: debug.SetMemoryLimit(-1)}
}

func (otherSystem) read() sample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return sample{at: time.Now(), capacity: float64(runtime.NumCPU()), load1: Unknown, memUsed: ms.Sys, memOK: true}
}
