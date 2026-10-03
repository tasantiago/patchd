package patch

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Fontes de reinício pendente no Linux.
const (
	rebootRequiredPath     = "/run/reboot-required"
	rebootRequiredPkgsPath = "/run/reboot-required.pkgs"
	bootDir                = "/boot"
	runningKernelPath      = "/proc/sys/kernel/osrelease"
	procStatPath           = "/proc/stat"
	kernelSignalSource     = "kernel em execução × mais novo em /boot"
)

// Reboot combina o arquivo do Debian/Ubuntu com a comparação de kernels (todas as distribuições).
func (s linuxScanner) Reboot(ctx context.Context) (protocol.RebootStatus, error) {
	st := protocol.RebootStatus{Signals: []protocol.RebootSignal{}}

	// O arquivo só existe no Debian e no Ubuntu: noutras distribuições, sua ausência não diz nada.
	if manager, _, _ := inventory.DetectPackageManager(s.readFile); manager == "dpkg" {
		sig := protocol.RebootSignal{Source: rebootRequiredPath}
		_, err := s.modTime(rebootRequiredPath)
		switch {
		case err == nil:
			sig.Pending = true
			if data, err := s.readFile(rebootRequiredPkgsPath); err == nil {
				st.Packages = parseRebootRequiredPkgs(data)
			}
		case errors.Is(err, fs.ErrNotExist):
			// Sem o arquivo: nada pediu reinício.
		default:
			sig.Error = err.Error()
		}
		st.Signals = append(st.Signals, sig)
	}

	st.Signals = append(st.Signals, s.kernelSignal(&st))

	if data, err := s.readFile(procStatPath); err == nil {
		if t, err := parseProcStatBtime(data); err == nil {
			st.LastBoot = &t
		}
	}

	st.Pending = rebootDecision(st.Signals)
	return st, nil
}

// kernelSignal compara o kernel em execução com o mais novo instalado em /boot.
func (s linuxScanner) kernelSignal(st *protocol.RebootStatus) protocol.RebootSignal {
	sig := protocol.RebootSignal{Source: kernelSignalSource}

	running, err := s.readFile(runningKernelPath)
	if err != nil {
		sig.Error = err.Error()
		return sig
	}
	st.RunningKernel = strings.TrimSpace(string(running))

	names, err := s.listDir(bootDir)
	if err != nil {
		sig.Error = err.Error()
		return sig
	}
	newest, ok := newestKernel(names)
	if !ok {
		sig.Error = "nenhum kernel (vmlinuz-*) em /boot"
		return sig
	}
	st.NewestKernel = newest
	sig.Pending = kernelNewer(newest, st.RunningKernel)
	return sig
}
