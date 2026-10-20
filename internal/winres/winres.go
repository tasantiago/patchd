// Package winres monta o recurso de versão (VERSIONINFO) do executável do Windows, num
// objeto COFF (.syso) que o linker do Go junta ao executável (Aula 20.9).
//
// Para que serve: o Windows mostra a "Descrição do arquivo" (FileDescription) desse recurso
// no título dos avisos da área de notificação, no Gerenciador de Tarefas e na lista de
// aplicativos de inicialização. Sem o recurso, aparece o nome do arquivo (patchd-app.exe).
//
// Sem ferramenta de fora: o formato é pequeno (a árvore de recursos com uma folha e o
// VS_VERSIONINFO) e o teste confere byte a byte o que o Windows lê.
package winres

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"unicode"
	"unicode/utf16"
)

// Info é o que vai no recurso.
type Info struct {
	Version          string // a versão do patchd (v0.17.13, v0.17.13-dev...): também em números
	FileDescription  string // o nome que o usuário vê (avisos, Gerenciador de Tarefas)
	ProductName      string
	CompanyName      string // opcional
	OriginalFilename string
	InternalName     string
	LegalCopyright   string // opcional
}

// LangPTBR e CodePageUnicode: a tabela de textos é em português do Brasil, em UTF-16.
const (
	LangPTBR        = 0x0416
	CodePageUnicode = 0x04B0
)

// MaxText: limite de cada texto (o Windows trunca descrições longas no aviso).
const MaxText = 80

var versionNumbers = regexp.MustCompile(`^v?(\d{1,5})\.(\d{1,5})\.(\d{1,5})`)

// Numbers devolve os quatro números da versão (o quarto é sempre 0); fora do formato: zeros.
func Numbers(v string) [4]uint16 {
	m := versionNumbers.FindStringSubmatch(v)
	var n [4]uint16
	if m == nil {
		return n
	}
	for i := 0; i < 3; i++ {
		x, err := strconv.ParseUint(m[i+1], 10, 16)
		if err != nil {
			return [4]uint16{}
		}
		n[i] = uint16(x)
	}
	return n
}

func checkText(name, s string, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s vazio", name)
		}
		return nil
	}
	if len([]rune(s)) > MaxText {
		return fmt.Errorf("%s com mais de %d caracteres", name, MaxText)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return fmt.Errorf("%s com caractere de controle ou inválido", name)
		}
	}
	return nil
}

// VersionInfo monta o VS_VERSIONINFO.
func VersionInfo(i Info) ([]byte, error) {
	for _, c := range []struct {
		name, v  string
		required bool
	}{{"FileDescription", i.FileDescription, true}, {"ProductName", i.ProductName, true}, {"Version", i.Version, true},
		{"CompanyName", i.CompanyName, false}, {"OriginalFilename", i.OriginalFilename, false},
		{"InternalName", i.InternalName, false}, {"LegalCopyright", i.LegalCopyright, false}} {
		if err := checkText(c.name, c.v, c.required); err != nil {
			return nil, err
		}
	}
	n := Numbers(i.Version)
	ms, ls := uint32(n[0])<<16|uint32(n[1]), uint32(n[2])<<16|uint32(n[3])
	var fixed bytes.Buffer
	for _, v := range []uint32{
		0xFEEF04BD, 0x00010000, // assinatura e versão da estrutura
		ms, ls, ms, ls, // FileVersion e ProductVersion
		0x3F, 0, // FileFlagsMask, FileFlags
		0x00040004, 1, 0, // VOS_NT_WINDOWS32, VFT_APP, sem subtipo
		0, 0, // data
	} {
		_ = binary.Write(&fixed, binary.LittleEndian, v)
	}

	var strs [][]byte
	for _, kv := range [][2]string{
		{"CompanyName", i.CompanyName}, {"FileDescription", i.FileDescription}, {"FileVersion", i.Version},
		{"InternalName", i.InternalName}, {"LegalCopyright", i.LegalCopyright}, {"OriginalFilename", i.OriginalFilename},
		{"ProductName", i.ProductName}, {"ProductVersion", i.Version},
	} {
		if kv[1] == "" {
			continue
		}
		val := utf16z(kv[1])
		strs = append(strs, node(kv[0], val, uint16(len(val)/2), 1, nil)) // #nosec G115 -- MaxText limita
	}
	table := node(fmt.Sprintf("%04X%04X", LangPTBR, CodePageUnicode), nil, 0, 1, strs)
	sfi := node("StringFileInfo", nil, 0, 1, [][]byte{table})
	var tr bytes.Buffer
	_ = binary.Write(&tr, binary.LittleEndian, [2]uint16{LangPTBR, CodePageUnicode})
	vfi := node("VarFileInfo", nil, 0, 1, [][]byte{node("Translation", tr.Bytes(), 4, 0, nil)})
	return node("VS_VERSION_INFO", fixed.Bytes(), uint16(fixed.Len()), 0, [][]byte{sfi, vfi}), nil // #nosec G115 -- 52 bytes
}

// node: wLength, wValueLength, wType, szKey, alinhamento, Value, e os filhos alinhados.
func node(key string, value []byte, valueLen, typ uint16, children [][]byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, valueLen, typ})
	b.Write(utf16z(key))
	pad4(&b)
	b.Write(value)
	for _, c := range children {
		pad4(&b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out))) // #nosec G115 -- recurso pequeno (testado)
	return out
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(out[2*i:], c)
	}
	return out
}

// Máquinas do COFF e o tipo de relocação "endereço relativo à imagem" de cada uma.
var machines = map[string]struct{ machine, reloc uint16 }{
	"amd64": {0x8664, 3}, // IMAGE_REL_AMD64_ADDR32NB
	"arm64": {0xAA64, 2}, // IMAGE_REL_ARM64_ADDR32NB
	"386":   {0x014C, 7}, // IMAGE_REL_I386_DIR32NB
}

// ErrArch: arquitetura sem formato conhecido.
var ErrArch = errors.New("arquitetura do Windows desconhecida (amd64, arm64 ou 386)")

const rtVersion = 16

// Syso devolve o objeto COFF com o recurso de versão (tipo RT_VERSION, número 1, pt-BR).
func Syso(goarch string, versionInfo []byte) ([]byte, error) {
	m, ok := machines[goarch]
	if !ok {
		return nil, ErrArch
	}
	// A seção .rsrc: três níveis de diretório (tipo, número, idioma), a folha e os dados.
	var sec bytes.Buffer
	dir := func(id uint32, offset uint32) {
		_ = binary.Write(&sec, binary.LittleEndian, struct {
			Characteristics, TimeDateStamp uint32
			Major, Minor, Named, IDs       uint16
			ID, Offset                     uint32
		}{IDs: 1, ID: id, Offset: offset})
	}
	const dirSize = 24
	dir(rtVersion, 0x80000000|dirSize) // tipo → nível 2
	dir(1, 0x80000000|2*dirSize)       // número 1 → nível 3
	dir(LangPTBR, 3*dirSize)           // idioma → a folha (sem o bit alto)
	const leaf, data = 3 * dirSize, 3*dirSize + 16
	_ = binary.Write(&sec, binary.LittleEndian, [4]uint32{data, uint32(len(versionInfo)), 0, 0}) // #nosec G115 -- recurso pequeno
	sec.Write(versionInfo)
	pad4(&sec)

	const fileHeader, sectionHeader, relocSize, symSize = 20, 40, 10, 18
	raw := fileHeader + sectionHeader
	relocs := raw + sec.Len()
	syms := relocs + relocSize
	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(struct {
		Machine, Sections               uint16
		Time, SymbolTable, Symbols      uint32
		OptionalHeader, Characteristics uint16
	}{Machine: m.machine, Sections: 1, SymbolTable: uint32(syms), Symbols: 1}) // #nosec G115 -- recurso pequeno
	w(struct {
		Name                                        [8]byte
		VirtualSize, VirtualAddress, SizeOfRaw, Raw uint32
		Relocs, Lines                               uint32
		NRelocs, NLines                             uint16
		Characteristics                             uint32
	}{Name: [8]byte{'.', 'r', 's', 'r', 'c'}, SizeOfRaw: uint32(sec.Len()), Raw: uint32(raw), // #nosec G115 -- recurso pequeno
		Relocs: uint32(relocs), NRelocs: 1, Characteristics: 0x40000040}) // #nosec G115 -- recurso pequeno
	out.Write(sec.Bytes())
	// A folha aponta para os dados pelo endereço na imagem final: o linker soma o da seção.
	w(struct {
		VirtualAddress, Symbol uint32
		Type                   uint16
	}{VirtualAddress: leaf, Symbol: 0, Type: m.reloc})
	w(struct {
		Name                 [8]byte
		Value                uint32
		Section              int16
		Type                 uint16
		StorageClass, NumAux uint8
	}{Name: [8]byte{'.', 'r', 's', 'r', 'c'}, Section: 1, StorageClass: 3})
	w(uint32(4)) // tabela de nomes vazia
	if out.Len() != syms+symSize+4 {
		return nil, fmt.Errorf("tamanho do objeto inesperado: %d", out.Len())
	}
	return out.Bytes(), nil
}
