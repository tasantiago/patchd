package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// Fontes do hardware no Linux.
const (
	dmiDir      = "/sys/class/dmi/id/"
	cpuinfoPath = "/proc/cpuinfo"
	meminfoPath = "/proc/meminfo"
)

// Hardware lê o DMI/SMBIOS em /sys, a CPU em /proc/cpuinfo e a memória em /proc/meminfo.
// Arquivo DMI inexistente (ARM, alguns ambientes virtuais) deixa o campo vazio;
// permissão negada (serial e UUID exigem root) vira erro parcial.
func (c *linuxCollector) Hardware(ctx context.Context) (Hardware, error) {
	var hw Hardware
	var errs []error
	dmiLido := false

	dmi := []struct {
		file string
		dst  *string
	}{
		{"sys_vendor", &hw.Manufacturer},
		{"product_name", &hw.Model},
		{"product_serial", &hw.SerialNumber},
		{"product_uuid", &hw.UUID},
		{"chassis_type", &hw.ChassisType},
		{"bios_vendor", &hw.BIOSVendor},
		{"bios_version", &hw.BIOSVersion},
		{"bios_date", &hw.BIOSDate},
	}
	for _, d := range dmi {
		data, err := c.readFile(dmiDir + d.file)
		switch {
		case err == nil:
			*d.dst = strings.TrimSpace(string(data))
			dmiLido = true
		case errors.Is(err, fs.ErrNotExist):
			// Sem esse dado nesta máquina: campo vazio, não é erro.
		case errors.Is(err, fs.ErrPermission):
			errs = append(errs, fmt.Errorf("%s: permissão negada (exige root)", dmiDir+d.file))
		default:
			errs = append(errs, fmt.Errorf("%s: %w", dmiDir+d.file, err))
		}
	}
	if dmiLido {
		hw.Sources = append(hw.Sources, dmiDir)
	}

	if data, err := c.readFile(cpuinfoPath); err == nil {
		hw.CPUModel, hw.CPUThreads = parseCPUInfo(data)
		hw.Sources = append(hw.Sources, cpuinfoPath)
	} else {
		errs = append(errs, fmt.Errorf("%s: %w", cpuinfoPath, err))
	}

	if data, err := c.readFile(meminfoPath); err == nil {
		if mem, err := parseMeminfo(data); err == nil {
			hw.MemoryBytes = mem
			hw.Sources = append(hw.Sources, meminfoPath)
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", meminfoPath, err))
		}
	} else {
		errs = append(errs, fmt.Errorf("%s: %w", meminfoPath, err))
	}

	return hw, errors.Join(errs...)
}
