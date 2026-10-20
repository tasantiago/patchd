package winres

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNumbers(t *testing.T) {
	for v, quer := range map[string][4]uint16{
		"v0.17.13": {0, 17, 13, 0}, "v0.17.13-dev": {0, 17, 13, 0}, "v1.2.3-4-gabcdef": {1, 2, 3, 0},
		"0.5.1": {0, 5, 1, 0}, "dev": {}, "abc1234-dirty": {}, "v99999.0.0": {},
	} {
		if got := Numbers(v); got != quer {
			t.Errorf("%s: %v", v, got)
		}
	}
}

// leNo lê um nó do VS_VERSIONINFO como o Windows lê: tamanho, chave, valor e filhos.
type no struct {
	key      string
	valueLen uint16
	typ      uint16
	value    []byte
	children []no
}

func leNo(t *testing.T, b []byte) no {
	t.Helper()
	n := int(binary.LittleEndian.Uint16(b))
	if n > len(b) || n < 6 {
		t.Fatalf("wLength %d fora de %d", n, len(b))
	}
	b = b[:n]
	var nd no
	nd.valueLen, nd.typ = binary.LittleEndian.Uint16(b[2:]), binary.LittleEndian.Uint16(b[4:])
	i := 6
	var u []uint16
	for ; ; i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			i += 2
			break
		}
		u = append(u, c)
	}
	nd.key = string(utf16.Decode(u))
	i = (i + 3) &^ 3
	vl := int(nd.valueLen)
	if nd.typ == 1 {
		vl *= 2 // texto: wValueLength em caracteres
	}
	if vl > 0 { // como o Windows: há valor quando wValueLength > 0
		nd.value = b[i : i+vl]
		i += vl
	}
	for {
		i = (i + 3) &^ 3
		if i >= len(b) {
			break
		}
		c := leNo(t, b[i:])
		nd.children = append(nd.children, c)
		i += int(binary.LittleEndian.Uint16(b[i:]))
	}
	return nd
}

func texto(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if c := binary.LittleEndian.Uint16(b[i:]); c != 0 {
			u = append(u, c)
		}
	}
	return string(utf16.Decode(u))
}

func TestVersionInfo(t *testing.T) {
	vi, err := VersionInfo(Info{Version: "v0.17.13", FileDescription: "Central de software", ProductName: "patchd",
		CompanyName: "Organização Exemplo", OriginalFilename: "patchd-agent.exe"})
	if err != nil {
		t.Fatal(err)
	}
	raiz := leNo(t, vi)
	if raiz.key != "VS_VERSION_INFO" || raiz.valueLen != 52 || len(raiz.value) != 52 ||
		binary.LittleEndian.Uint32(raiz.value) != 0xFEEF04BD || binary.LittleEndian.Uint32(raiz.value[8:]) != 17 ||
		binary.LittleEndian.Uint32(raiz.value[12:]) != 13<<16 {
		t.Fatalf("raiz: %+v", raiz)
	}
	if len(raiz.children) != 2 || raiz.children[0].key != "StringFileInfo" || raiz.children[1].key != "VarFileInfo" {
		t.Fatalf("filhos: %+v", raiz.children)
	}
	tabela := raiz.children[0].children[0]
	if tabela.key != "041604B0" {
		t.Errorf("tabela %q", tabela.key)
	}
	got := map[string]string{}
	for _, s := range tabela.children {
		got[s.key] = texto(s.value)
		if s.key == "FileDescription" {
			if s.valueLen != DescSlot {
				t.Errorf("FileDescription: espaço %d", s.valueLen)
			}
			continue
		}
		if int(s.valueLen) != len(utf16.Encode([]rune(got[s.key])))+1 {
			t.Errorf("%s: wValueLength %d", s.key, s.valueLen)
		}
	}
	if got["FileDescription"] != "Central de software" || got["CompanyName"] != "Organização Exemplo" ||
		got["FileVersion"] != "v0.17.13" || got["ProductName"] != "patchd" {
		t.Errorf("textos: %v", got)
	}
	if _, ok := got["LegalCopyright"]; ok {
		t.Error("texto vazio não entra")
	}
	tr := raiz.children[1].children[0]
	if tr.key != "Translation" || binary.LittleEndian.Uint32(tr.value) != CodePageUnicode<<16|LangPTBR {
		t.Errorf("Translation: %+v", tr)
	}

	for nome, i := range map[string]Info{
		"sem nome":     {Version: "v1.0.0", ProductName: "patchd"},
		"controle":     {Version: "v1.0.0", ProductName: "patchd", FileDescription: "Central\x1b[31m"},
		"longo":        {Version: "v1.0.0", ProductName: "patchd", FileDescription: strings.Repeat("a", MaxText+1)},
		"sem versão":   {ProductName: "patchd", FileDescription: "x"},
		"empresa ruim": {Version: "v1.0.0", ProductName: "patchd", FileDescription: "x", CompanyName: "a\nb"},
	} {
		if _, err := VersionInfo(i); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}

// TestSyso confere o objeto COFF campo a campo; o teste de verdade (o linker do Go junta o
// .syso e o Windows lê o recurso) foi feito na aula com o executável e o leitor de PE.
func TestSyso(t *testing.T) {
	vi, _ := VersionInfo(Info{Version: "v1.2.3", FileDescription: "Central de software", ProductName: "patchd"})
	for arch, quer := range map[string][2]uint16{"amd64": {0x8664, 3}, "arm64": {0xAA64, 2}} {
		o, err := Syso(arch, vi)
		if err != nil {
			t.Fatal(err)
		}
		le16 := func(i int) uint16 { return binary.LittleEndian.Uint16(o[i:]) }
		le32 := func(i int) uint32 { return binary.LittleEndian.Uint32(o[i:]) }
		if le16(0) != quer[0] || le16(2) != 1 || le32(12) != 1 || string(o[20:25]) != ".rsrc" {
			t.Fatalf("%s: cabeçalho", arch)
		}
		raw, rawLen, relocs := int(le32(20+20)), int(le32(20+16)), int(le32(20+24))
		sec := o[raw : raw+rawLen]
		// Tipo 16 (versão) → número 1 → idioma pt-BR → folha com os dados.
		if binary.LittleEndian.Uint32(sec[16:]) != 16 || binary.LittleEndian.Uint32(sec[40:]) != 1 ||
			binary.LittleEndian.Uint32(sec[64:]) != LangPTBR || binary.LittleEndian.Uint32(sec[68:]) != 72 {
			t.Errorf("%s: diretório", arch)
		}
		off, size := int(binary.LittleEndian.Uint32(sec[72:])), int(binary.LittleEndian.Uint32(sec[76:]))
		if string(sec[off:off+size]) != string(vi) {
			t.Errorf("%s: os dados da folha não são o VS_VERSIONINFO", arch)
		}
		if le32(relocs) != 72 || le32(relocs+4) != 0 || le16(relocs+8) != quer[1] {
			t.Errorf("%s: relocação", arch)
		}
	}
	if _, err := Syso("mips", vi); err == nil {
		t.Error("arquitetura desconhecida aceita")
	}
}

// TestDescricao (Aula 20.10): o nome da Central muda na cópia sem mexer no resto.
func TestDescricao(t *testing.T) {
	vi, _ := VersionInfo(Info{Version: "v0.17.14", FileDescription: "Central de software", ProductName: "patchd"})
	obj, _ := Syso("amd64", vi)
	exe := append(append([]byte("MZ cabeçalho falso "), obj...), []byte(" resto do executável")...)
	if d, ok := Description(exe); !ok || d != "Central de software" {
		t.Fatalf("lida: %q %v", d, ok)
	}
	novo, err := SetDescription(exe, "Atualizações da TI 🛠")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := Description(novo); d != "Atualizações da TI 🛠" || len(novo) != len(exe) {
		t.Errorf("trocada: %q (%d → %d bytes)", d, len(exe), len(novo))
	}
	diff := 0
	for i := range exe {
		if exe[i] != novo[i] {
			diff++
		}
	}
	if diff == 0 || diff > 2*DescSlot {
		t.Errorf("bytes mudados: %d", diff)
	}
	// Mais curto: o resto do espaço vira zero (nada do nome anterior aparece).
	curto, _ := SetDescription(novo, "TI")
	if d, _ := Description(curto); d != "TI" {
		t.Errorf("curto: %q", d)
	}
	if d, _ := Description(curto); strings.Contains(d, "Atualiza") {
		t.Error("sobrou o nome anterior")
	}
	for _, ruim := range []string{"", "a\x00b", strings.Repeat("x", MaxText+1)} {
		if _, err := SetDescription(exe, ruim); err == nil {
			t.Errorf("%q aceito", ruim)
		}
	}
	if _, ok := Description([]byte("sem recurso")); ok {
		t.Error("achou espaço onde não há")
	}
	dois := append(append(bytes.Clone(exe), make([]byte, len(exe)%4)...), obj...)
	if _, err := SetDescription(dois, "x"); err != ErrNoSlot {
		t.Errorf("dois espaços: %v", err)
	}
}
