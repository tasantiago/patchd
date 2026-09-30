package inventory

import "testing"

func TestParseMeminfo(t *testing.T) {
	dados := "MemTotal:       16318560 kB\nMemFree:         1234567 kB\n"
	got, err := parseMeminfo([]byte(dados))
	if err != nil || got != 16318560*1024 {
		t.Errorf("parseMeminfo = %d, %v; esperado %d", got, err, uint64(16318560*1024))
	}
	if _, err := parseMeminfo([]byte("MemFree: 1 kB\n")); err == nil {
		t.Error("esperado erro sem MemTotal")
	}
	if _, err := parseMeminfo([]byte("MemTotal: muito\n")); err == nil {
		t.Error("esperado erro com MemTotal fora do formato")
	}
}

func TestParseCPUInfoX86(t *testing.T) {
	dados := "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Core(TM) i5-10500 CPU @ 3.10GHz\n\n" +
		"processor\t: 1\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Core(TM) i5-10500 CPU @ 3.10GHz\n"
	model, threads := parseCPUInfo([]byte(dados))
	if model != "Intel(R) Core(TM) i5-10500 CPU @ 3.10GHz" || threads != 2 {
		t.Errorf("modelo=%q threads=%d", model, threads)
	}
}

func TestParseCPUInfoARMSemModelo(t *testing.T) {
	dados := "processor\t: 0\nCPU implementer\t: 0x41\n\nprocessor\t: 1\nCPU implementer\t: 0x41\n"
	model, threads := parseCPUInfo([]byte(dados))
	if model != "" || threads != 2 {
		t.Errorf("em ARM, esperado modelo vazio e 2 processadores: %q, %d", model, threads)
	}
}
