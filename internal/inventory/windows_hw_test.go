package inventory

import "testing"

func TestWindowsHardwareInfo(t *testing.T) {
	w := wmiHardware{
		Product: map[string]any{
			"Vendor": "LENOVO", "Name": "11XXABCDBP", "IdentifyingNumber": "XXXXXXXX",
			"UUID": "00000000-1111-2222-3333-444444444444",
		},
		BIOS: map[string]any{
			"Manufacturer": "LENOVO", "SMBIOSBIOSVersion": "M3AKT5AA", "ReleaseDate": "20250115000000.000000+000",
		},
		Enclosure:  map[string]any{"ChassisTypes": []any{int32(3)}},
		System:     map[string]any{"TotalPhysicalMemory": "17016795136"},
		Processors: []map[string]any{{"Name": " Intel(R) Core(TM) i5-10500 CPU @ 3.10GHz ", "NumberOfLogicalProcessors": int32(12)}},
	}
	hw := windowsHardwareInfo(w)

	if hw.Manufacturer != "LENOVO" || hw.Model != "11XXABCDBP" || hw.SerialNumber != "XXXXXXXX" {
		t.Errorf("identificação errada: %+v", hw)
	}
	if hw.BIOSVersion != "M3AKT5AA" || hw.BIOSDate != "20250115000000.000000+000" {
		t.Errorf("BIOS errado (a data segue crua, no formato do WMI): %+v", hw)
	}
	if hw.ChassisType != "3" {
		t.Errorf("chassi deveria vir do primeiro item do array: %q", hw.ChassisType)
	}
	if hw.MemoryBytes != 17016795136 {
		t.Errorf("memória em texto deveria ser convertida: %d", hw.MemoryBytes)
	}
	if hw.CPUModel != "Intel(R) Core(TM) i5-10500 CPU @ 3.10GHz" || hw.CPUThreads != 12 {
		t.Errorf("CPU errada: %+v", hw)
	}
	if len(hw.Sources) != 5 {
		t.Errorf("esperadas 5 fontes WMI: %v", hw.Sources)
	}
}

func TestWindowsHardwareDoisSoquetesESemClasses(t *testing.T) {
	w := wmiHardware{
		Processors: []map[string]any{
			{"Name": "Xeon", "NumberOfLogicalProcessors": uint32(16)},
			{"Name": "Xeon", "NumberOfLogicalProcessors": uint32(16)},
		},
	}
	hw := windowsHardwareInfo(w)
	if hw.CPUThreads != 32 {
		t.Errorf("dois soquetes deveriam somar: %d", hw.CPUThreads)
	}
	if hw.Manufacturer != "" || len(hw.Sources) != 1 {
		t.Errorf("classes ausentes deixam campos vazios e sem fonte: %+v", hw)
	}
}

func TestAsUint64(t *testing.T) {
	casos := []struct {
		v    any
		quer uint64
		ok   bool
	}{
		{"17016795136", 17016795136, true},
		{int32(12), 12, true},
		{uint16(3), 3, true},
		{int32(-1), 0, false},
		{"não é número", 0, false},
		{nil, 0, false},
	}
	for _, c := range casos {
		got, ok := asUint64(c.v)
		if ok != c.ok || (ok && got != c.quer) {
			t.Errorf("asUint64(%#v) = (%d, %t), esperado (%d, %t)", c.v, got, ok, c.quer, c.ok)
		}
	}
}
