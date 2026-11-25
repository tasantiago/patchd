package texto

import "testing"

func TestPlaceName(t *testing.T) {
	for raw, want := range map[string]string{
		"TRIBUNAL SEDE": "Tribunal Sede",
		"FÓRUM GERAL": "Fórum Geral",
		"  FÓRUM DE PORTO VELHO  ": "Fórum de Porto Velho",
		"ANEXO II DO TJRO": "Anexo II do TJRO",
		"101": "101",
		"": "",
	} {
		if got := PlaceName(raw); got != want {
			t.Errorf("PlaceName(%q) = %q; want %q", raw, got, want)
		}
	}
}
