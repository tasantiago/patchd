package inventory

import (
	"fmt"
	"strconv"
	"strings"
)

// parseMeminfo extrai o MemTotal do /proc/meminfo ("MemTotal:  16318560 kB") em bytes.
func parseMeminfo(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "MemTotal" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 || f[1] != "kB" {
			return 0, fmt.Errorf("MemTotal fora do formato: %q", line)
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("MemTotal inválido: %q", line)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("MemTotal não encontrado")
}

// parseCPUInfo devolve o modelo da CPU (x86: "model name") e a quantidade de
// processadores lógicos (linhas "processor"). Em ARM não há "model name": fica vazio.
func parseCPUInfo(data []byte) (model string, threads int) {
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "processor":
			threads++
		case "model name":
			if model == "" {
				model = strings.TrimSpace(value)
			}
		}
	}
	return model, threads
}
