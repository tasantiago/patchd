package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// Caminhos do os-release segundo a especificação: o primeiro vence; o segundo é a alternativa.
var osReleasePaths = []string{"/etc/os-release", "/usr/lib/os-release"}

const kernelReleasePath = "/proc/sys/kernel/osrelease"

// linuxCollector coleta o inventário no Linux.
type linuxCollector struct {
	run      platform.Runner              // comandos (dpkg-query, rpm)
	readFile func(string) ([]byte, error) // os.ReadFile; substituível nos testes
	hostname func() (string, error)       // os.Hostname; substituível nos testes
}

// New devolve o coletor do SO atual.
func New(run platform.Runner) Collector {
	return &linuxCollector{run: run, readFile: os.ReadFile, hostname: os.Hostname}
}

// readOSRelease lê e interpreta o primeiro os-release encontrado.
func (c *linuxCollector) readOSRelease() (map[string]string, string, error) {
	var errs []error
	for _, p := range osReleasePaths {
		d, err := c.readFile(p)
		if err == nil {
			return parseOSRelease(d), p, nil
		}
		errs = append(errs, err)
	}
	return nil, "", fmt.Errorf("os-release não encontrado: %w", errors.Join(errs...))
}

// OS lê o os-release (fonte de verdade da distribuição) e a versão do kernel.
func (c *linuxCollector) OS(ctx context.Context) (OSInfo, error) {
	info := OSInfo{Family: "linux", Arch: runtime.GOARCH}

	fields, src, err := c.readOSRelease()
	if err != nil {
		return info, err
	}
	info.ID = fields["ID"]
	info.Name = fields["NAME"]
	info.Version = fields["VERSION_ID"]
	info.Codename = fields["VERSION_CODENAME"]
	info.Sources = append(info.Sources, src)

	// O kernel é informativo: se não der para ler, o inventário segue sem ele.
	if k, err := c.readFile(kernelReleasePath); err == nil {
		info.Kernel = strings.TrimSpace(string(k))
		info.Sources = append(info.Sources, kernelReleasePath)
	}
	if h, err := c.hostname(); err == nil {
		info.Hostname = h
	}
	return info, nil
}
