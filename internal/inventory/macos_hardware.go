package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// Caminhos absolutos no macOS (o Runner recusa nomes relativos).
const (
	systemProfilerPath = "/usr/sbin/system_profiler"
	sysctlPath         = "/usr/sbin/sysctl"
)

// spHardware é o recorte de "system_profiler SPHardwareDataType -json" usado pelo inventário.
// Memória e processadores vêm de lá como texto formatado ("64 GB"); os números saem do sysctl.
type spHardware struct {
	Items []struct {
		MachineModel   string `json:"machine_model"`
		SerialNumber   string `json:"serial_number"`
		PlatformUUID   string `json:"platform_UUID"`
		BootROMVersion string `json:"boot_rom_version"`
	} `json:"SPHardwareDataType"`
}

// parseSPHardware extrai modelo (identificador, ex.: "Mac14,14"), serial, UUID de plataforma e firmware.
func parseSPHardware(data []byte) (Hardware, error) {
	var sp spHardware
	if err := json.Unmarshal(data, &sp); err != nil {
		return Hardware{}, fmt.Errorf("SPHardwareDataType: JSON inválido: %w", err)
	}
	if len(sp.Items) == 0 {
		return Hardware{}, errors.New("SPHardwareDataType: resposta sem itens")
	}
	it := sp.Items[0]
	return Hardware{
		Model:        it.MachineModel,
		SerialNumber: it.SerialNumber,
		UUID:         it.PlatformUUID,
		BIOSVersion:  it.BootROMVersion,
	}, nil
}

// parseSysctlHardware interpreta "sysctl -n hw.memsize hw.ncpu machdep.cpu.brand_string":
// uma linha por chave, na ordem pedida.
func parseSysctlHardware(data []byte) (mem uint64, ncpu int, brand string, err error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		return 0, 0, "", fmt.Errorf("sysctl: esperadas 3 linhas, vieram %d", len(lines))
	}
	mem, err = strconv.ParseUint(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil {
		return 0, 0, "", fmt.Errorf("sysctl hw.memsize inválido: %q", lines[0])
	}
	ncpu, err = strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil {
		return 0, 0, "", fmt.Errorf("sysctl hw.ncpu inválido: %q", lines[1])
	}
	return mem, ncpu, strings.TrimSpace(lines[2]), nil
}

// collectDarwinHardware monta o Hardware do macOS a partir do system_profiler e do sysctl.
// Fica fora do arquivo _darwin.go para ser testável em qualquer SO. Falha de uma fonte
// não impede a outra: o resultado parcial volta junto com o erro.
func collectDarwinHardware(ctx context.Context, run platform.Runner) (Hardware, error) {
	// Todo Mac é hardware e firmware da Apple.
	hw := Hardware{Manufacturer: "Apple", BIOSVendor: "Apple"}
	var errs []error

	if out, err := run.Run(ctx, systemProfilerPath, "SPHardwareDataType", "-json"); err != nil {
		errs = append(errs, err)
	} else if sp, err := parseSPHardware(out); err != nil {
		errs = append(errs, err)
	} else {
		hw.Model, hw.SerialNumber, hw.UUID, hw.BIOSVersion = sp.Model, sp.SerialNumber, sp.UUID, sp.BIOSVersion
		hw.Sources = append(hw.Sources, systemProfilerPath+" SPHardwareDataType")
	}

	if out, err := run.Run(ctx, sysctlPath, "-n", "hw.memsize", "hw.ncpu", "machdep.cpu.brand_string"); err != nil {
		errs = append(errs, err)
	} else if mem, ncpu, brand, err := parseSysctlHardware(out); err != nil {
		errs = append(errs, err)
	} else {
		hw.MemoryBytes, hw.CPUThreads, hw.CPUModel = mem, ncpu, brand
		hw.Sources = append(hw.Sources, sysctlPath)
	}

	return hw, errors.Join(errs...)
}
