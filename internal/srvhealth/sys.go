package srvhealth

import (
	"math"
	"time"
)

// sample é uma leitura do sistema; os deltas entre duas dão o minuto.
type sample struct {
	at       time.Time
	capacity float64 // os núcleos que o processo pode usar

	procCPU time.Duration // CPU do processo, acumulada
	procOK  bool

	hostBusy, hostTotal uint64 // /proc/stat, em ticks
	hostOK              bool

	memUsed, memLimit       uint64 // o que conta contra o limite, e o limite
	memOK                   bool
	hostAvail, hostTotalMem uint64
	hostMemOK               bool

	load1 float64
}

// system lê o sistema (Linux: /proc e cgroup; outros: quase nada).
type system interface {
	read() sample
	info() Info
}

func cpuPct(prev, cur sample) float64 {
	wall := cur.at.Sub(prev.at)
	if !prev.procOK || !cur.procOK || wall <= 0 || cur.capacity <= 0 {
		return Unknown
	}
	used := cur.procCPU - prev.procCPU
	if used < 0 {
		return Unknown
	}
	return clampPct(100 * float64(used) / (float64(wall) * cur.capacity))
}

func hostCPUPct(prev, cur sample) float64 {
	if !prev.hostOK || !cur.hostOK || cur.hostTotal <= prev.hostTotal || cur.hostBusy < prev.hostBusy {
		return Unknown
	}
	return clampPct(100 * float64(cur.hostBusy-prev.hostBusy) / float64(cur.hostTotal-prev.hostTotal))
}

// memOf: os bytes em uso, a % do limite e a % da memória da máquina em uso.
func memOf(s sample) (used uint64, pct, host float64) {
	pct, host = Unknown, Unknown
	if s.memOK {
		used = s.memUsed
		if s.memLimit > 0 {
			pct = clampPct(100 * float64(s.memUsed) / float64(s.memLimit))
		}
	}
	if s.hostMemOK && s.hostTotalMem > 0 && s.hostAvail <= s.hostTotalMem {
		host = clampPct(100 * float64(s.hostTotalMem-s.hostAvail) / float64(s.hostTotalMem))
	}
	return used, pct, host
}

func clampPct(v float64) float64 { return math.Max(0, math.Min(100, v)) }
