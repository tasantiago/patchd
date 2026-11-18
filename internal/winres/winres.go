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
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

// DescSlot: o espaço da descrição, em unidades UTF-16, com o zero final. Cabem MaxText
// caracteres mesmo que todos precisem de duas unidades (fora do plano básico).
const DescSlot = 2*MaxText + 1

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
	// A descrição vai num espaço de tamanho fixo (DescSlot), com zeros depois do texto: o
	// agente troca o nome na cópia da Central sem mexer em mais nada do executável
	// (SetDescription, Aula 20.10). Quem lê o recurso para no primeiro zero.
	desc := make([]byte, 2*DescSlot)
	copy(desc, utf16z(i.FileDescription))
	strs = append(strs, node("FileDescription", desc, DescSlot, 1, nil))
	for _, kv := range [][2]string{
		{"CompanyName", i.CompanyName}, {"FileVersion", i.Version},
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

// Tipos de recurso usados.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
)

// Resource é uma folha da árvore de recursos: tipo, número e idioma.
type Resource struct {
	Type, ID, Lang uint16
	Data           []byte
}

// Syso devolve o objeto COFF com o recurso de versão (tipo RT_VERSION, número 1, pt-BR).
func Syso(goarch string, versionInfo []byte) ([]byte, error) {
	return SysoResources(goarch, []Resource{{Type: rtVersion, ID: 1, Lang: LangPTBR, Data: versionInfo}})
}

// VersionResource: o recurso de versão para SysoResources.
func VersionResource(versionInfo []byte) Resource {
	return Resource{Type: rtVersion, ID: 1, Lang: LangPTBR, Data: versionInfo}
}

// maxSection: o maior .rsrc (o ícone com os tamanhos até 256 cabe com folga).
const maxSection = 4 << 20

// SysoResources devolve o objeto COFF com a árvore de recursos (o linker do Go aceita uma
// seção .rsrc: tudo vai junto). Um idioma por tipo e número.
func SysoResources(goarch string, res []Resource) ([]byte, error) {
	m, ok := machines[goarch]
	if !ok {
		return nil, ErrArch
	}
	if len(res) == 0 || len(res) > 64 {
		return nil, errors.New("de 1 a 64 recursos")
	}
	res = slices.Clone(res)
	slices.SortFunc(res, func(a, b Resource) int { return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID)) })
	var types []uint16
	for i, r := range res {
		if i > 0 && r.Type == res[i-1].Type && r.ID == res[i-1].ID {
			return nil, fmt.Errorf("recurso %d/%d repetido", r.Type, r.ID)
		}
		if len(r.Data) == 0 {
			return nil, fmt.Errorf("recurso %d/%d vazio", r.Type, r.ID)
		}
		if len(types) == 0 || types[len(types)-1] != r.Type {
			types = append(types, r.Type)
		}
	}
	// A seção .rsrc: os diretórios (raiz → tipo → número → idioma), as folhas e os dados.
	const dirHeader, dirEntry, leafSize = 16, 8, 16
	typeOff := map[uint16]int{}
	at := dirHeader + dirEntry*len(types)
	for _, t := range types {
		typeOff[t] = at
		at += dirHeader + dirEntry*countType(res, t)
	}
	idOff := make([]int, len(res))
	for i := range res {
		idOff[i] = at
		at += dirHeader + dirEntry
	}
	leafOff := make([]int, len(res))
	for i := range res {
		leafOff[i] = at
		at += leafSize
	}
	var sec bytes.Buffer
	dir := func(entries [][2]uint32) {
		_ = binary.Write(&sec, binary.LittleEndian, struct {
			Characteristics, TimeDateStamp uint32
			Major, Minor, Named, IDs       uint16
		}{IDs: uint16(len(entries))}) // #nosec G115 -- até 64
		for _, e := range entries {
			_ = binary.Write(&sec, binary.LittleEndian, e)
		}
	}
	var root [][2]uint32
	for _, t := range types {
		root = append(root, [2]uint32{uint32(t), 0x80000000 | uint32(typeOff[t])}) // #nosec G115 -- seção pequena
	}
	dir(root)
	for _, t := range types {
		var ids [][2]uint32
		for i, r := range res {
			if r.Type == t {
				ids = append(ids, [2]uint32{uint32(r.ID), 0x80000000 | uint32(idOff[i])}) // #nosec G115 -- seção pequena
			}
		}
		dir(ids)
	}
	for i, r := range res {
		dir([][2]uint32{{uint32(r.Lang), uint32(leafOff[i])}}) // #nosec G115 -- seção pequena; idioma sem o bit alto: a folha
	}
	// Os dados vêm depois das folhas, cada um alinhado a 8.
	dataOff := make([]int, len(res))
	pos := at
	for i, r := range res {
		pos = (pos + 7) &^ 7
		dataOff[i] = pos
		pos += len(r.Data)
	}
	if pos > maxSection {
		return nil, errors.New("recursos grandes demais")
	}
	for i, r := range res {
		_ = binary.Write(&sec, binary.LittleEndian, [4]uint32{uint32(dataOff[i]), uint32(len(r.Data)), 0, 0}) // #nosec G115 -- maxSection
	}
	for i, r := range res {
		for sec.Len() < dataOff[i] {
			sec.WriteByte(0)
		}
		sec.Write(r.Data)
	}
	pad4(&sec)

	const fileHeader, sectionHeader, relocSize, symSize = 20, 40, 10, 18
	raw := fileHeader + sectionHeader
	relocs := raw + sec.Len()
	syms := relocs + relocSize*len(res)
	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(struct {
		Machine, Sections               uint16
		Time, SymbolTable, Symbols      uint32
		OptionalHeader, Characteristics uint16
	}{Machine: m.machine, Sections: 1, SymbolTable: uint32(syms), Symbols: 1}) // #nosec G115 -- maxSection
	w(struct {
		Name                                        [8]byte
		VirtualSize, VirtualAddress, SizeOfRaw, Raw uint32
		Relocs, Lines                               uint32
		NRelocs, NLines                             uint16
		Characteristics                             uint32
	}{Name: [8]byte{'.', 'r', 's', 'r', 'c'}, SizeOfRaw: uint32(sec.Len()), Raw: uint32(raw), // #nosec G115 -- maxSection
		Relocs: uint32(relocs), NRelocs: uint16(len(res)), Characteristics: 0x40000040}) // #nosec G115 -- maxSection, até 64
	out.Write(sec.Bytes())
	// Cada folha aponta para os dados pelo endereço na imagem final: o linker soma o da seção.
	for _, l := range leafOff {
		w(struct {
			VirtualAddress, Symbol uint32
			Type                   uint16
		}{VirtualAddress: uint32(l), Symbol: 0, Type: m.reloc}) // #nosec G115 -- seção pequena
	}
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

func countType(res []Resource, t uint16) int {
	n := 0
	for _, r := range res {
		if r.Type == t {
			n++
		}
	}
	return n
}

// IconGroup é o número do grupo do ícone do executável: o primeiro grupo é o que o Explorer
// mostra, e a Central o carrega para o ícone perto do relógio.
const IconGroup = 1

// maxIcon: o maior .ico aceito.
const maxIcon = 2 << 20

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// IconResources lê um .ico e devolve os recursos do ícone do executável: uma imagem RT_ICON
// por tamanho (números 1, 2...) e o RT_GROUP_ICON IconGroup que as lista. Cada imagem é um
// PNG ou um DIB (BITMAPINFOHEADER), como no .ico.
func IconResources(ico []byte) ([]Resource, error) {
	if len(ico) < 6 || len(ico) > maxIcon {
		return nil, errors.New("ícone: arquivo vazio ou maior que 2 MiB")
	}
	le16 := func(i int) uint16 { return binary.LittleEndian.Uint16(ico[i:]) }
	le32 := func(i int) uint32 { return binary.LittleEndian.Uint32(ico[i:]) }
	n := int(le16(4))
	if le16(0) != 0 || le16(2) != 1 || n == 0 || n > 32 || len(ico) < 6+16*n {
		return nil, errors.New("ícone: não é um .ico (cabeçalho)")
	}
	var group bytes.Buffer
	_ = binary.Write(&group, binary.LittleEndian, [3]uint16{0, 1, uint16(n)}) // #nosec G115 -- até 32
	var out []Resource
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		size, off := le32(e+8), le32(e+12)
		if size < 40 || uint64(off)+uint64(size) > uint64(len(ico)) {
			return nil, fmt.Errorf("ícone: a imagem %d sai do arquivo", i+1)
		}
		img := ico[off : off+size]
		if !bytes.HasPrefix(img, pngMagic) && binary.LittleEndian.Uint32(img) != 40 {
			return nil, fmt.Errorf("ícone: a imagem %d não é PNG nem DIB", i+1)
		}
		id := uint16(i + 1) // #nosec G115 -- até 32
		out = append(out, Resource{Type: rtIcon, ID: id, Data: bytes.Clone(img)})
		group.Write(ico[e : e+12]) // largura, altura, cores, reservado, planos, bits, tamanho
		_ = binary.Write(&group, binary.LittleEndian, id)
	}
	return append(out, Resource{Type: rtGroupIcon, ID: IconGroup, Data: group.Bytes()}), nil
}

// ErrNoSlot: o executável não tem o recurso de versão com o espaço da descrição (um build
// sem o scripts/build.sh, por exemplo).
var ErrNoSlot = errors.New("o executável não tem o espaço da descrição no recurso de versão")

// descKey: a chave do texto, em UTF-16 com o zero, como fica no recurso.
var descKey = utf16z("FileDescription")

// descSlot acha o espaço da descrição: o nó String com a chave, tipo texto e o tamanho fixo.
// Devolve onde começa o valor. Mais de um candidato também é ErrNoSlot (não adivinha).
func descSlot(exe []byte) (int, error) {
	found := -1
	for i := 0; ; {
		j := bytes.Index(exe[i:], descKey)
		if j < 0 {
			break
		}
		k := i + j
		i = k + 1
		if k < 6 || k%2 != 0 {
			continue
		}
		wLen := binary.LittleEndian.Uint16(exe[k-6:])
		vLen := binary.LittleEndian.Uint16(exe[k-4:])
		typ := binary.LittleEndian.Uint16(exe[k-2:])
		val := k - 6 + (6+len(descKey)+3)&^3 // o valor vem alinhado a 4 a partir do início do nó
		if typ != 1 || vLen != DescSlot || int(wLen) != val-(k-6)+2*DescSlot || val+2*DescSlot > len(exe) {
			continue
		}
		if found >= 0 {
			return 0, ErrNoSlot
		}
		found = val
	}
	if found < 0 {
		return 0, ErrNoSlot
	}
	return found, nil
}

// Description lê a descrição gravada no executável (ok=false: sem o espaço).
func Description(exe []byte) (string, bool) {
	at, err := descSlot(exe)
	if err != nil {
		return "", false
	}
	u := make([]uint16, 0, DescSlot)
	for i := 0; i < DescSlot; i++ {
		c := binary.LittleEndian.Uint16(exe[at+2*i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), true
}

// SetDescription devolve uma cópia do executável com outra descrição no mesmo espaço; o resto
// do arquivo fica igual, byte a byte.
func SetDescription(exe []byte, name string) ([]byte, error) {
	if err := checkText("FileDescription", name, true); err != nil {
		return nil, err
	}
	at, err := descSlot(exe)
	if err != nil {
		return nil, err
	}
	out := bytes.Clone(exe)
	slot := out[at : at+2*DescSlot]
	clear(slot)
	copy(slot, utf16z(name))
	return out, nil
}
