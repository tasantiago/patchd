package perf

import (
	"context"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

var runner platform.Runner = platform.ExecRunner{}

// readCPU: a soma do %cpu dos processos pelos núcleos (o macOS não expõe os contadores sem cgo).
func readCPU(ctx context.Context) (cpuTimes, error) {
	n, err := runner.Run(ctx, "/usr/sbin/sysctl", "-n", "hw.ncpu")
	if err != nil {
		return cpuTimes{}, err
	}
	cores, _ := strconv.Atoi(strings.TrimSpace(string(n)))
	out, err := runner.Run(ctx, "/bin/ps", "-A", "-o", "%cpu=")
	if err != nil {
		return cpuTimes{}, err
	}
	pct, err := parsePSCPU(string(out), cores)
	return cpuTimes{pct: pct, direct: true}, err
}

func readMem(ctx context.Context) (float64, error) {
	m, err := runner.Run(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, err
	}
	size, err := strconv.ParseUint(strings.TrimSpace(string(m)), 10, 64)
	if err != nil {
		return 0, err
	}
	out, err := runner.Run(ctx, "/usr/bin/vm_stat")
	if err != nil {
		return 0, err
	}
	return parseVMStat(string(out), size)
}
