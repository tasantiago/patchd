package inventory

import (
	"context"
	"io/fs"
	"strings"
	"testing"
)

// arquivosComErros é como arquivosFalsos, mas permite simular erros por caminho.
func arquivosComErros(arquivos map[string]string, erros map[string]error) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if err, ok := erros[p]; ok {
			return nil, err
		}
		if c, ok := arquivos[p]; ok {
			return []byte(c), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestLinuxHardwareUsuarioComum(t *testing.T) {
	c := &linuxCollector{readFile: arquivosComErros(
		map[string]string{
			dmiDir + "sys_vendor":   "LENOVO\n",
			dmiDir + "product_name": "11XXABCD\n",
			dmiDir + "chassis_type": "3\n",
			dmiDir + "bios_version": "M3AKT5AA\n",
			cpuinfoPath:             "processor\t: 0\nmodel name\t: CPU Teste\nprocessor\t: 1\n",
			meminfoPath:             "MemTotal:       8000000 kB\n",
		},
		map[string]error{
			dmiDir + "product_serial": fs.ErrPermission,
			dmiDir + "product_uuid":   fs.ErrPermission,
		},
	)}

	hw, err := c.Hardware(context.Background())
	if hw.Manufacturer != "LENOVO" || hw.Model != "11XXABCD" || hw.ChassisType != "3" || hw.BIOSVersion != "M3AKT5AA" {
		t.Errorf("campos DMI errados: %+v", hw)
	}
	if hw.CPUModel != "CPU Teste" || hw.CPUThreads != 2 || hw.MemoryBytes != 8000000*1024 {
		t.Errorf("CPU ou memória errados: %+v", hw)
	}
	if hw.SerialNumber != "" || hw.UUID != "" {
		t.Errorf("serial e UUID deveriam ficar vazios sem root: %+v", hw)
	}
	if err == nil || !strings.Contains(err.Error(), "product_serial") || !strings.Contains(err.Error(), "exige root") {
		t.Errorf("esperado erro parcial explicando a falta de root: %v", err)
	}
	if len(hw.Sources) != 3 {
		t.Errorf("esperadas 3 fontes (DMI, cpuinfo, meminfo): %v", hw.Sources)
	}
}

func TestLinuxHardwareSemDMI(t *testing.T) {
	c := &linuxCollector{readFile: arquivosFalsos(map[string]string{
		cpuinfoPath: "processor\t: 0\n",
		meminfoPath: "MemTotal: 1024 kB\n",
	})}
	hw, err := c.Hardware(context.Background())
	if err != nil {
		t.Errorf("máquina sem DMI não é erro: %v", err)
	}
	if hw.Manufacturer != "" || len(hw.Sources) != 2 {
		t.Errorf("sem DMI, só as fontes de CPU e memória: %+v", hw)
	}
}
