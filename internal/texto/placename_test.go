package texto

import "testing"

func TestPlaceName(t *testing.T) {
	SetPlaceAcronyms("")
	casos := map[string]string{
		"TRIBUNAL SEDE":        "Tribunal Sede",
		"FÓRUM GERAL":          "Fórum Geral",
		"  FÓRUM DE EXEMPLO  ": "Fórum de Exemplo",
		"ANEXO II DA SEDE":     "Anexo II da Sede",
		"BLOCO A":              "Bloco A",
		"Bloco A":              "Bloco A",
		"TORRE B - SALA DA TI": "Torre B - Sala da TI",
		"CASA DE A":            "Casa de A",
		"NÚCLEO XYZ":           "Núcleo Xyz", // sigla da instituição sem o parâmetro: palavra comum
		"101":                  "101",
		"":                     "",
	}
	for raw, want := range casos {
		if got := PlaceName(raw); got != want {
			t.Errorf("PlaceName(%q) = %q; want %q", raw, got, want)
		}
	}
	// As siglas da instituição vêm do parâmetro do painel.
	SetPlaceAcronyms("xyz; abc1, XYZ")
	defer SetPlaceAcronyms("")
	if got := PlaceName("NÚCLEO XYZ DO ABC1"); got != "Núcleo XYZ do ABC1" {
		t.Errorf("com o parâmetro: %q", got)
	}
}

func TestParsePlaceAcronyms(t *testing.T) {
	if l, err := ParsePlaceAcronyms(" abc, def;GHI  abc "); err != nil || len(l) != 3 || l[0] != "ABC" {
		t.Fatalf("%v %v", l, err)
	}
	for _, ruim := range []string{"A-B", "ABCDEFGHIJKLM", "<script>"} {
		if _, err := ParsePlaceAcronyms(ruim); err == nil {
			t.Errorf("%q aceito", ruim)
		}
	}
}
