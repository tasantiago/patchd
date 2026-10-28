package patrimonio

import (
	"slices"
	"strings"
	"testing"
)

// Os dados são fictícios; o formato é o do sistema de patrimônio.
const exemplo = "DEPREDIO,DESALA,UO,TOMBO,DEBEM,DESUBGRUPOBEM\n" +
	"FORUM EXEMPLO,SALA 101 - CARTORIO,UOX,106259,WORKSTATION EXEMPLO 64GB,COMPUTADOR\n" +
	"FORUM EXEMPLO,\"SALA 102, ANEXO\",UOX,088594,DESKTOP EXEMPLO,COMPUTADOR\n" +
	"FORUM EXEMPLO,SALA 103,UOX,,MONITOR SEM TOMBO,MONITOR\n" +
	"FORUM EXEMPLO,SALA 104,UOX,10A2,TOMBO ERRADO,COMPUTADOR\n" +
	"\n" +
	"ANEXO EXEMPLO,  SALA   201  ,UOY,106259,WORKSTATION EXEMPLO 64GB,COMPUTADOR\n"

func TestParse(t *testing.T) {
	r, err := Parse(strings.NewReader(exemplo))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 || r.Skipped != 2 || r.Duplicates != 1 || r.Separator != ',' || r.Converted {
		t.Fatalf("resultado: %+v", r)
	}
	if !slices.Equal(r.SkippedAt, []int{4, 5}) {
		t.Errorf("linhas puladas: %v", r.SkippedAt)
	}
	// Repetido: vale a última linha; espaços repetidos viram um.
	if r.Items[0] != (Item{Tombo: "106259", Building: "ANEXO EXEMPLO", Room: "SALA 201", Unit: "UOY",
		Description: "WORKSTATION EXEMPLO 64GB", Subgroup: "COMPUTADOR"}) {
		t.Errorf("repetido: %+v", r.Items[0])
	}
	// Zeros à esquerda saem; a vírgula entre aspas fica no campo.
	if r.Items[1].Tombo != "88594" || r.Items[1].Room != "SALA 102, ANEXO" {
		t.Errorf("zeros e aspas: %+v", r.Items[1])
	}
}

func TestParseFormatosDoExcel(t *testing.T) {
	// Ponto e vírgula, colunas em outra ordem, cabeçalho em minúsculas, BOM e Windows-1252.
	cp := "tombo;depredio;desala\r\n123;F\xd3RUM;ADMINISTRA\xc7\xc3O\r\n"
	r, err := Parse(strings.NewReader(cp))
	if err != nil {
		t.Fatal(err)
	}
	if r.Separator != ';' || !r.Converted || len(r.Items) != 1 || r.Items[0] != (Item{Tombo: "123", Building: "FÓRUM", Room: "ADMINISTRAÇÃO"}) {
		t.Fatalf("Excel: %+v", r)
	}
	u, err := Parse(strings.NewReader("\xef\xbb\xbfTOMBO,DESALA\n7,SALÃO\n"))
	if err != nil || u.Converted || len(u.Items) != 1 || u.Items[0].Room != "SALÃO" {
		t.Fatalf("UTF-8 com BOM: %+v %v", u, err)
	}
}

func TestParseRecusa(t *testing.T) {
	for nome, in := range map[string]string{
		"sem tombo no cabeçalho": "DEPREDIO,DESALA\nA,B\n",
		"vazio":                  "",
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: aceitou", nome)
		}
	}
	if _, err := Parse(strings.NewReader("TOMBO\n" + strings.Repeat("x", MaxBytes))); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("grande demais: %v", err)
	}
}

func TestTombosEMatch(t *testing.T) {
	casos := map[string][]string{
		"FJ088594":    {"88594"},
		"FJ102874":    {"102874"},
		"102874-NB":   {"102874"},
		"NB-102874-2": {"102874", "2"},
		"SRV0000":     nil,
		"desktop-abc": nil,
		"PC112345":    {"112345"},
	}
	for host, want := range casos {
		if got := Tombos(host); !slices.Equal(got, want) {
			t.Errorf("Tombos(%q) = %v, esperava %v", host, got, want)
		}
	}
	bens := map[string]string{"88594": "sala A", "1234": "sala B", "2": "sala C"}
	if v, ok := Match("FJ088594", bens); !ok || v != "sala A" {
		t.Errorf("FJ088594: %q %t", v, ok)
	}
	// O tombo é a sequência inteira: 1234 não liga PC112345.
	if v, ok := Match("PC112345", bens); ok {
		t.Errorf("PC112345 casou com %q", v)
	}
	// A primeira sequência que for um tombo.
	if v, ok := Match("NB-999999-2", bens); !ok || v != "sala C" {
		t.Errorf("NB-999999-2: %q %t", v, ok)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{" 00123 ": "123", "0": "", "12a": "", "": "", strings.Repeat("9", 21): ""} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, esperava %q", in, got, want)
		}
	}
}
