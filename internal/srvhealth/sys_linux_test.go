package srvhealth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsers(t *testing.T) {
	busy, total, ok := parseProcStat("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3 4\n")
	if !ok || busy != 150 || total != 1000 {
		t.Fatalf("proc/stat: %d de %d (%v)", busy, total, ok)
	}
	if _, _, ok := parseProcStat("intr 1 2 3"); ok {
		t.Fatal("linha errada aceita")
	}
	tot, av := parseMeminfo(strings.NewReader("MemTotal:  8000 kB\nMemFree: 10 kB\nMemAvailable:  2000 kB\n"))
	if tot != 8000*1024 || av != 2000*1024 {
		t.Fatalf("meminfo %d %d", tot, av)
	}
	if q := parseCPUMax("200000 100000\n"); q != 2 {
		t.Fatalf("cpu.max = %v", q)
	}
	if q := parseCPUMax("max 100000"); q != 0 {
		t.Fatalf("sem cota = %v", q)
	}
	if p, ok := parseCgroupV2("12:cpu:/x\n0::/system.slice/patchd.service\n"); !ok || p != "/system.slice/patchd.service" {
		t.Fatalf("cgroup %q %v", p, ok)
	}
}

// Um container de mentira: cgroup v2 com 2 núcleos de cota e 1 GiB de limite.
func TestLinuxContainer(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/self/cgroup", "0::/\n")
	write("sys/fs/cgroup/memory.current", "536870912\n")
	write("sys/fs/cgroup/memory.max", "1073741824\n")
	write("sys/fs/cgroup/cpu.max", "200000 100000\n")
	write("proc/meminfo", "MemTotal: 16777216 kB\nMemAvailable: 8388608 kB\n")
	write("proc/stat", "cpu 10 0 10 80 0 0 0 0\n")
	write("proc/loadavg", "0.50 0.40 0.30 1/100 1\n")
	s := &linuxSystem{root: root}
	s.cgroup = s.findCgroup()
	in := s.info()
	if !in.MemFromCG || in.MemLimit != 1<<30 || in.HostMemTotal != 16<<30 {
		t.Fatalf("info %+v", in)
	}
	if in.CPUQuota != 2 || (in.Cores > 2 && in.Capacity != 2) {
		t.Fatalf("cota %v capacidade %v", in.CPUQuota, in.Capacity)
	}
	smp := s.read()
	used, pct, host := memOf(smp)
	if used != 512<<20 || pct != 50 || host != 50 || smp.load1 != 0.5 {
		t.Fatalf("memória %d %v %v carga %v", used, pct, host, smp.load1)
	}
}
