package inventory

import (
	"fmt"
	"strconv"
	"strings"
)

// wmiHardware são os valores lidos do WMI (root\cimv2) para o hardware do Windows.
// Fica fora do arquivo _windows.go para ser testável em qualquer SO.
type wmiHardware struct {
	Product    map[string]any   // Win32_ComputerSystemProduct: Vendor, Name, IdentifyingNumber, UUID
	BIOS       map[string]any   // Win32_BIOS: Manufacturer, SMBIOSBIOSVersion, ReleaseDate
	Enclosure  map[string]any   // Win32_SystemEnclosure: ChassisTypes (array)
	System     map[string]any   // Win32_ComputerSystem: TotalPhysicalMemory
	Processors []map[string]any // Win32_Processor: Name, NumberOfLogicalProcessors (um por soquete)
}

// windowsHardwareInfo monta o Hardware a partir dos valores do WMI. Valores crus: a data
// do BIOS segue no formato do WMI e o serial vai como está (a filtragem fica no servidor).
func windowsHardwareInfo(w wmiHardware) Hardware {
	var hw Hardware

	if w.Product != nil {
		hw.Manufacturer = asString(w.Product["Vendor"])
		hw.Model = asString(w.Product["Name"])
		hw.SerialNumber = asString(w.Product["IdentifyingNumber"])
		hw.UUID = asString(w.Product["UUID"])
		hw.Sources = append(hw.Sources, `WMI root\cimv2 Win32_ComputerSystemProduct`)
	}
	if w.BIOS != nil {
		hw.BIOSVendor = asString(w.BIOS["Manufacturer"])
		hw.BIOSVersion = asString(w.BIOS["SMBIOSBIOSVersion"])
		hw.BIOSDate = asString(w.BIOS["ReleaseDate"])
		hw.Sources = append(hw.Sources, `WMI root\cimv2 Win32_BIOS`)
	}
	if w.Enclosure != nil {
		if types, ok := w.Enclosure["ChassisTypes"].([]any); ok && len(types) > 0 {
			if n, ok := asUint64(types[0]); ok {
				hw.ChassisType = strconv.FormatUint(n, 10)
			}
		}
		hw.Sources = append(hw.Sources, `WMI root\cimv2 Win32_SystemEnclosure`)
	}
	if w.System != nil {
		if n, ok := asUint64(w.System["TotalPhysicalMemory"]); ok {
			hw.MemoryBytes = n
		}
		hw.Sources = append(hw.Sources, `WMI root\cimv2 Win32_ComputerSystem`)
	}
	if len(w.Processors) > 0 {
		hw.CPUModel = asString(w.Processors[0]["Name"])
		for _, p := range w.Processors {
			if n, ok := asUint64(p["NumberOfLogicalProcessors"]); ok {
				hw.CPUThreads += int(n)
			}
		}
		hw.Sources = append(hw.Sources, `WMI root\cimv2 Win32_Processor`)
	}
	return hw
}

// asString converte um valor do WMI em texto sem espaços nas pontas; nulo vira "".
func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// asUint64 aceita números de qualquer largura e também texto: pela interface de scripting
// do WMI, inteiros de 64 bits costumam chegar como string.
func asUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint8:
		return uint64(n), true
	case uint16:
		return uint64(n), true
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	case int8:
		return uint64(n), n >= 0
	case int16:
		return uint64(n), n >= 0
	case int32:
		return uint64(n), n >= 0
	case int64:
		return uint64(n), n >= 0
	case int:
		return uint64(n), n >= 0
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		return u, err == nil
	default:
		return 0, false
	}
}
