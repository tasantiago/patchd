package srvhealth

import (
	"bufio"
	"io"
	"math"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// O Linux: a CPU do processo pelo getrusage, a da máquina pelo /proc/stat, a memória pelo
// cgroup v2 do processo (no container, o limite do container) ou, sem limite, pelo RSS contra a
// memória da máquina (/proc/meminfo).

type linuxSystem struct {
	root   string // "/" (os testes trocam)
	cgroup string // a pasta do cgroup v2 do processo; "" sem cgroup v2
}

func newSystem() system {
	s := &linuxSystem{root: "/"}
	s.cgroup = s.findCgroup()
	return s
}

func (s *linuxSystem) file(name string) string { return path.Join(s.root, name) }

func (s *linuxSystem) readFile(name string) (string, bool) {
	// #nosec G304 -- caminhos fixos do /proc e do /sys
	b, err := os.ReadFile(s.file(name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// findCgroup lê /proc/self/cgroup ("0::/caminho", o cgroup v2) e confere que a pasta existe.
func (s *linuxSystem) findCgroup() string {
	txt, ok := s.readFile("proc/self/cgroup")
	if !ok {
		return ""
	}
	rel, ok := parseCgroupV2(txt)
	if !ok {
		return ""
	}
	dir := path.Join("sys/fs/cgroup", rel)
	if _, err := os.Stat(s.file(path.Join(dir, "memory.current"))); err != nil {
		// Num container com o próprio namespace de cgroup, o caminho visto é "/"; fora dele o
		// caminho do host pode não estar montado: sem cgroup.
		return ""
	}
	return dir
}

func parseCgroupV2(txt string) (string, bool) {
	for _, line := range strings.Split(txt, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return path.Clean("/" + strings.TrimSpace(rest)), true
		}
	}
	return "", false
}

// cgMemLimit: memory.max ("max": sem limite).
func (s *linuxSystem) cgMemLimit() uint64 {
	if s.cgroup == "" {
		return 0
	}
	txt, ok := s.readFile(path.Join(s.cgroup, "memory.max"))
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(txt), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// cgCPUQuota: cpu.max ("200000 100000": 2 núcleos; "max 100000": sem cota). Sobe na árvore:
// a cota pode estar no pai (o slice do container).
func (s *linuxSystem) cgCPUQuota() float64 {
	for dir := s.cgroup; dir != "" && dir != "sys/fs" && dir != "."; dir = path.Dir(dir) {
		if txt, ok := s.readFile(path.Join(dir, "cpu.max")); ok {
			if q := parseCPUMax(txt); q > 0 {
				return q
			}
		}
		if dir == "sys/fs/cgroup" {
			break
		}
	}
	return 0
}

func parseCPUMax(txt string) float64 {
	f := strings.Fields(txt)
	if len(f) != 2 || f[0] == "max" {
		return 0
	}
	q, err1 := strconv.ParseFloat(f[0], 64)
	p, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || q <= 0 || p <= 0 {
		return 0
	}
	return q / p
}

func (s *linuxSystem) info() Info {
	in := Info{Cores: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), OS: runtime.GOOS, GoMemLimit: debug.SetMemoryLimit(-1)}
	in.CPUQuota = s.cgCPUQuota()
	in.Capacity = float64(in.Cores)
	if in.CPUQuota > 0 && in.CPUQuota < in.Capacity {
		in.Capacity = in.CPUQuota
	}
	if txt, ok := s.readFile("proc/meminfo"); ok {
		in.HostMemTotal, _ = parseMeminfo(strings.NewReader(txt))
	}
	in.MemLimit = in.HostMemTotal
	if l := s.cgMemLimit(); l > 0 && (in.HostMemTotal == 0 || l < in.HostMemTotal) {
		in.MemLimit, in.MemFromCG = l, true
	}
	return in
}

func (s *linuxSystem) read() sample {
	in := s.info()
	out := sample{at: time.Now(), capacity: in.Capacity, load1: Unknown}
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err == nil {
		out.procCPU = time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
		out.procOK = true
	}
	if txt, ok := s.readFile("proc/stat"); ok {
		out.hostBusy, out.hostTotal, out.hostOK = parseProcStat(txt)
	}
	if txt, ok := s.readFile("proc/meminfo"); ok {
		out.hostTotalMem, out.hostAvail = parseMeminfo(strings.NewReader(txt))
		out.hostMemOK = out.hostTotalMem > 0
	}
	if txt, ok := s.readFile("proc/loadavg"); ok {
		if f := strings.Fields(txt); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				out.load1 = v
			}
		}
	}
	// A memória que conta: a do cgroup, se ele tem limite (no container, é o que mata o
	// processo); senão o RSS do processo contra a memória da máquina.
	if in.MemFromCG {
		if txt, ok := s.readFile(path.Join(s.cgroup, "memory.current")); ok {
			if n, err := strconv.ParseUint(strings.TrimSpace(txt), 10, 64); err == nil {
				out.memUsed, out.memLimit, out.memOK = n, in.MemLimit, true
			}
		}
	}
	if !out.memOK {
		if txt, ok := s.readFile("proc/self/statm"); ok {
			if f := strings.Fields(txt); len(f) > 1 {
				if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
					// #nosec G115 -- tamanho de página: positivo
					out.memUsed, out.memLimit, out.memOK = pages*uint64(os.Getpagesize()), in.HostMemTotal, true
				}
			}
		}
	}
	return out
}

// parseProcStat: a primeira linha ("cpu  user nice system idle iowait irq softirq steal ...").
// Ocupado = tudo menos idle e iowait.
func parseProcStat(txt string) (busy, total uint64, ok bool) {
	line, _, _ := strings.Cut(txt, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	var idle uint64
	for i, v := range f[1:] {
		if i >= 8 { // guest e guest_nice já estão em user
			break
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return total - idle, total, total > 0
}

// parseMeminfo: MemTotal e MemAvailable, em bytes.
func parseMeminfo(r io.Reader) (total, avail uint64) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || n > math.MaxUint64/1024 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	return total, avail
}
