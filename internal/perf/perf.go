// Package perf mede CPU, memória e disco a cada minuto (docs/monitoramento.md, seção 2). A
// parte de cada sistema (perf_windows.go, perf_linux.go, perf_darwin.go) lê os contadores; esta
// faz as contas e é testável em qualquer sistema.
package perf

import (
	"bufio"
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// cpuTimes: os contadores acumulados de CPU (Windows e Linux), ou a porcentagem já pronta
// (macOS, direct).
type cpuTimes struct {
	busy, total uint64
	pct         float64
	direct      bool
}

// Sampler guarda a leitura anterior da CPU: a porcentagem de um minuto é a diferença entre
// duas leituras.
type Sampler struct {
	prev    cpuTimes
	has     bool
	readCPU func(ctx context.Context) (cpuTimes, error)
	readMem func(ctx context.Context) (float64, error)
	sleep   func(ctx context.Context, d time.Duration) error
}

// New devolve o medidor deste sistema.
func New() *Sampler {
	return &Sampler{readCPU: readCPU, readMem: readMem, sleep: sleepCtx}
}

// ErrUnsupported: este sistema não mede desempenho.
var ErrUnsupported = errors.New("medida de desempenho não suportada neste sistema")

// Sample mede agora. disks são os volumes da coleta do estado da máquina (hoststate).
func (s *Sampler) Sample(ctx context.Context, at time.Time, disks []protocol.Disk) (protocol.PerfSample, error) {
	out := protocol.PerfSample{At: at.UTC().Truncate(time.Second), Disks: DiskFree(disks)}
	cur, err := s.readCPU(ctx)
	if err != nil {
		return out, err
	}
	if !cur.direct && !s.has {
		// Primeira medida: um segundo de janela, para não esperar o próximo minuto.
		s.prev, s.has = cur, true
		if err := s.sleep(ctx, time.Second); err != nil {
			return out, err
		}
		if cur, err = s.readCPU(ctx); err != nil {
			return out, err
		}
	}
	out.CPU = cpuPct(s.prev, cur)
	s.prev, s.has = cur, true
	if out.Mem, err = s.readMem(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// cpuPct: a porcentagem entre duas leituras, de 0 a 100.
func cpuPct(prev, cur cpuTimes) float64 {
	if cur.direct {
		return clamp(cur.pct)
	}
	if cur.total <= prev.total || cur.busy < prev.busy {
		return 0 // contador reiniciado (hibernação) ou leitura igual
	}
	return clamp(float64(cur.busy-prev.busy) * 100 / float64(cur.total-prev.total))
}

// DiskFree: % livre de cada volume com tamanho conhecido.
func DiskFree(disks []protocol.Disk) map[string]float64 {
	out := map[string]float64{}
	for _, d := range disks {
		if d.TotalBytes == 0 || d.Mount == "" || len(d.Mount) > protocol.MaxPerfMount || len(out) == protocol.MaxPerfDisks {
			continue
		}
		out[d.Mount] = round1(float64(d.FreeBytes) * 100 / float64(d.TotalBytes))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clamp(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return round1(v)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// parseProcStat lê a primeira linha do /proc/stat ("cpu user nice system idle iowait irq
// softirq steal ..."). Ocioso = idle + iowait; o guest já está dentro de user.
func parseProcStat(data string) (cpuTimes, error) {
	line, _, _ := strings.Cut(data, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, errors.New("/proc/stat sem a linha cpu")
	}
	var total, idle uint64
	for i, s := range f[1:] {
		if i >= 8 {
			break
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return cpuTimes{}, errors.New("/proc/stat ilegível")
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return cpuTimes{busy: total - idle, total: total}, nil
}

// parseMeminfo: % em uso pelo /proc/meminfo (MemTotal e MemAvailable, em kB).
func parseMeminfo(data string) (float64, error) {
	var total, avail uint64
	var hasAvail bool
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail, hasAvail = n, true
		}
	}
	if total == 0 || !hasAvail || avail > total {
		return 0, errors.New("/proc/meminfo sem MemTotal ou MemAvailable")
	}
	return clamp(float64(total-avail) * 100 / float64(total)), nil
}

// parseVMStat: % em uso no macOS, pelo vm_stat (páginas livres, inativas e especulativas
// contam como disponíveis) e o total de hw.memsize.
func parseVMStat(data string, memsize uint64) (float64, error) {
	pageSize := uint64(4096)
	free := uint64(0)
	found := 0
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "page size of") {
			for _, f := range strings.Fields(line) {
				if n, err := strconv.ParseUint(f, 10, 64); err == nil && n > 0 {
					pageSize = n
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Pages free", "Pages inactive", "Pages speculative":
			n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
			if err != nil {
				return 0, errors.New("vm_stat ilegível")
			}
			free += n
			found++
		}
	}
	if found == 0 || memsize == 0 {
		return 0, errors.New("vm_stat sem as páginas livres")
	}
	avail := free * pageSize
	if avail > memsize {
		return 0, nil
	}
	return clamp(float64(memsize-avail) * 100 / float64(memsize)), nil
}

// parsePSCPU: a soma do %cpu de cada processo (ps -A -o %cpu=) dividida pelos núcleos.
func parsePSCPU(data string, cores int) (float64, error) {
	if cores < 1 {
		return 0, errors.New("número de núcleos desconhecido")
	}
	var sum float64
	for _, f := range strings.Fields(data) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(f, ",", "."), 64)
		if err != nil {
			return 0, errors.New("ps ilegível")
		}
		sum += v
	}
	return clamp(sum / float64(cores)), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
