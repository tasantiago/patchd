package texto

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"Instalação concluída": "Instalação concluída",
		"pc\x00lab":            "pc�lab",
		"a\xffb":               "a�b",
		"\xc3":                 "�",
		"":                     "",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"ação", 2, "a"}, // "ç" tem 2 bytes: cortar em 2 partiria ao meio
		{"ação", 3, "aç"},
		{"ação", 100, "ação"},
		{"abc", 0, ""},
		{"\x00\x00", 3, "�"},
	} {
		if got := Clip(c.in, c.n); got != c.want {
			t.Errorf("Clip(%q, %d) = %q, esperado %q", c.in, c.n, got, c.want)
		}
	}
}

func TestCleanAll(t *testing.T) {
	type item struct {
		Nome    string
		Tags    []string
		Extra   map[string]any
		Bytes   []byte
		segredo string
	}
	v := struct {
		Host  string
		Itens []item
		Ptr   *item
		Mapa  map[string]string
	}{
		Host: "pc\x00",
		Itens: []item{{Nome: "ok", Tags: []string{"a\xff"}, Extra: map[string]any{"k\x00": []any{"x\x00", 1.0}},
			Bytes: []byte{0}, segredo: "\x00"}},
		Ptr:  &item{Nome: "\xc3"},
		Mapa: map[string]string{"a": "b\x00"},
	}
	if n := CleanAll(&v); n != 6 {
		t.Errorf("mudou %d, esperado 6", n)
	}
	if v.Host != "pc�" || v.Itens[0].Tags[0] != "a�" || v.Ptr.Nome != "�" || v.Mapa["a"] != "b�" {
		t.Errorf("não limpou: %+v", v)
	}
	if l, ok := v.Itens[0].Extra["k�"].([]any); !ok || l[0] != "x�" {
		t.Errorf("mapa com any: %#v", v.Itens[0].Extra)
	}
	if v.Itens[0].Bytes[0] != 0 || v.Itens[0].segredo != "\x00" {
		t.Error("[]byte e campos não exportados ficam como estão")
	}
}

// FuzzClip: para qualquer texto e limite, o resultado é UTF-8 válido, sem NUL, cabe no
// limite e é um começo do texto limpo (nada inventado no meio).
func FuzzClip(f *testing.F) {
	f.Add("Instalação concluída com êxito", 10)
	f.Add("\xff\x00ç", 2)
	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 || n > 1<<16 {
			return
		}
		c := Clip(s, n)
		if !utf8.ValidString(c) || strings.ContainsRune(c, 0) || len(c) > n || !strings.HasPrefix(Clean(s), c) {
			t.Fatalf("Clip(%q, %d) = %q", s, n, c)
		}
	})
}
