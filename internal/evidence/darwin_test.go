package evidence

import "testing"

func TestParseHardwareIdentity(t *testing.T) {
	dados := `{"SPHardwareDataType":[{"machine_model":"Mac14,14","platform_UUID":"A1B2C3D4-E5F6-4711-8899-AABBCCDDEEFF","serial_number":"XYZ123ABC4"}]}`
	u, s, err := parseHardwareIdentity([]byte(dados))
	if err != nil || u != "A1B2C3D4-E5F6-4711-8899-AABBCCDDEEFF" || s != "XYZ123ABC4" {
		t.Errorf("= %q %q %v", u, s, err)
	}
	if _, _, err := parseHardwareIdentity([]byte(`{"SPHardwareDataType":[]}`)); err == nil {
		t.Error("sem itens deveria ser erro")
	}
}
