// Package authsig lê, sem conferir, quem assinou um instalador do Windows: a assinatura
// Authenticode de um executável (PE) ou de um MSI (o fluxo \x05DigitalSignature do arquivo
// composto). Serve para o painel preencher e mostrar a organização (O=) que a regra exige.
//
// Não é conferência: a cadeia, o conteúdo e a raiz quem confere é o agente, com o
// WinVerifyTrust do Windows (internal/wintrust). Um arquivo forjado pode dizer aqui qualquer
// O=; o agente o recusa do mesmo jeito.
package authsig

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"unicode/utf16"
)

// Signer é quem assinou: o certificado do signatário.
type Signer struct {
	Organization string // o primeiro O= do Subject (o que o agente compara)
	CommonName   string
	Subject      string // o Subject inteiro, para mostrar
}

var (
	// ErrUnsigned: o arquivo não tem assinatura Authenticode.
	ErrUnsigned = errors.New("o arquivo não tem assinatura")
	// ErrFormat: nem executável (PE) nem MSI.
	ErrFormat = errors.New("o arquivo não é um executável nem um MSI")
)

const maxSig = 1 << 20 // a assinatura: algumas dezenas de KB; 1 MiB é folga

var cfbMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// Read lê o signatário do instalador.
func Read(r io.ReaderAt, size int64) (Signer, error) {
	head := make([]byte, 8)
	if _, err := r.ReadAt(head, 0); err != nil {
		return Signer{}, fmt.Errorf("ler o começo do arquivo: %w", err)
	}
	var der []byte
	var err error
	switch {
	case bytes.HasPrefix(head, []byte("MZ")):
		der, err = peSignature(r, size)
	case bytes.Equal(head, cfbMagic):
		der, err = msiSignature(r, size)
	default:
		return Signer{}, ErrFormat
	}
	if err != nil {
		return Signer{}, err
	}
	return signerOf(der)
}

func readAt(r io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > size {
		return nil, fmt.Errorf("leitura fora do arquivo (%d+%d de %d)", off, n, size)
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil && !(errors.Is(err, io.EOF) && off+int64(n) == size) {
		return nil, err
	}
	return b, nil
}

// peSignature: a tabela de certificados do executável (o diretório de segurança, cujo
// endereço é posição no arquivo) e, nela, o primeiro WIN_CERTIFICATE PKCS#7.
func peSignature(r io.ReaderAt, size int64) ([]byte, error) {
	dos, err := readAt(r, size, 0, 64)
	if err != nil {
		return nil, err
	}
	pe := int64(binary.LittleEndian.Uint32(dos[0x3C:]))
	hdr, err := readAt(r, size, pe, 24+2)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(hdr[:4], []byte("PE\x00\x00")) {
		return nil, ErrFormat
	}
	opt := pe + 24
	var dirs int64
	switch binary.LittleEndian.Uint16(hdr[24:]) {
	case 0x10b: // PE32
		dirs = 96
	case 0x20b: // PE32+
		dirs = 112
	default:
		return nil, ErrFormat
	}
	n, err := readAt(r, size, opt+dirs-4, 4)
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(n) <= 4 {
		return nil, ErrUnsigned
	}
	sec, err := readAt(r, size, opt+dirs+4*8, 8)
	if err != nil {
		return nil, err
	}
	off, length := int64(binary.LittleEndian.Uint32(sec)), int64(binary.LittleEndian.Uint32(sec[4:]))
	if off == 0 || length == 0 {
		return nil, ErrUnsigned
	}
	if length > maxSig {
		return nil, fmt.Errorf("tabela de certificados de %d bytes", length)
	}
	table, err := readAt(r, size, off, int(length))
	if err != nil {
		return nil, err
	}
	for len(table) >= 8 {
		l := int(binary.LittleEndian.Uint32(table))
		typ := binary.LittleEndian.Uint16(table[6:])
		if l < 8 || l > len(table) {
			break
		}
		if typ == 0x0002 { // WIN_CERT_TYPE_PKCS_SIGNED_DATA
			return table[8:l], nil
		}
		l = (l + 7) &^ 7
		if l > len(table) {
			break
		}
		table = table[l:]
	}
	return nil, ErrUnsigned
}

// --- MSI: o arquivo composto (Compound File Binary) ------------------------------------

const (
	cfbEnd    = 0xFFFFFFFE
	cfbFree   = 0xFFFFFFFF
	dirEntry  = 128
	maxDirEnt = 1 << 16
)

type cfb struct {
	r            io.ReaderAt
	size         int64
	shift        uint
	ssize        int64
	difat        []uint32 // as entradas do cabeçalho (109) e, sob demanda, as dos setores DIFAT
	nextDifat    uint32
	firstMiniFAT uint32
	miniCutoff   uint32
	fatCache     map[uint32][]byte
	maxSteps     int // setores que cabem no arquivo: nenhuma cadeia passa disso (arquivo em laço)
}

func (c *cfb) sectorOff(sid uint32) int64 { return (int64(sid) + 1) << c.shift }

func (c *cfb) sector(sid uint32) ([]byte, error) {
	if b, ok := c.fatCache[sid]; ok {
		return b, nil
	}
	b, err := readAt(c.r, c.size, c.sectorOff(sid), int(c.ssize))
	if err != nil {
		return nil, err
	}
	if len(c.fatCache) < 4096 {
		c.fatCache[sid] = b
	}
	return b, nil
}

// fatSector: o setor da FAT de número i (pela DIFAT, lida sob demanda).
func (c *cfb) fatSector(i int) (uint32, error) {
	per := int(c.ssize/4) - 1
	for i >= len(c.difat) {
		if c.nextDifat == cfbEnd || c.nextDifat == cfbFree || len(c.difat) > 109+c.maxSteps*per {
			return 0, errors.New("FAT incompleta")
		}
		b, err := c.sector(c.nextDifat)
		if err != nil {
			return 0, err
		}
		for k := 0; k < per; k++ {
			c.difat = append(c.difat, binary.LittleEndian.Uint32(b[k*4:]))
		}
		c.nextDifat = binary.LittleEndian.Uint32(b[per*4:])
	}
	return c.difat[i], nil
}

// next: o setor seguinte da cadeia, pela FAT.
func (c *cfb) next(sid uint32) (uint32, error) {
	per := uint32(c.ssize / 4)
	fs, err := c.fatSector(int(sid / per))
	if err != nil {
		return 0, err
	}
	b, err := c.sector(fs)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[(sid%per)*4:]), nil
}

// chain lê n bytes de uma cadeia de setores.
func (c *cfb) chain(start uint32, n int64) ([]byte, error) {
	var out []byte
	sid := start
	for steps := 0; int64(len(out)) < n; steps++ {
		if sid == cfbEnd || sid == cfbFree || steps > c.maxSteps {
			return nil, errors.New("cadeia de setores interrompida")
		}
		b, err := readAt(c.r, c.size, c.sectorOff(sid), int(c.ssize))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if sid, err = c.next(sid); err != nil {
			return nil, err
		}
	}
	return out[:n], nil
}

func msiSignature(r io.ReaderAt, size int64) ([]byte, error) {
	h, err := readAt(r, size, 0, 512)
	if err != nil {
		return nil, err
	}
	c := &cfb{r: r, size: size, fatCache: map[uint32][]byte{}}
	c.shift = uint(binary.LittleEndian.Uint16(h[0x1E:]))
	if c.shift != 9 && c.shift != 12 {
		return nil, ErrFormat
	}
	c.ssize = 1 << c.shift
	c.maxSteps = int(size>>c.shift) + 1
	miniShift := uint(binary.LittleEndian.Uint16(h[0x20:]))
	firstDir := binary.LittleEndian.Uint32(h[0x30:])
	c.miniCutoff = binary.LittleEndian.Uint32(h[0x38:])
	c.firstMiniFAT = binary.LittleEndian.Uint32(h[0x3C:])
	c.nextDifat = binary.LittleEndian.Uint32(h[0x44:])
	for k := 0; k < 109; k++ {
		c.difat = append(c.difat, binary.LittleEndian.Uint32(h[0x4C+k*4:]))
	}

	// O diretório: a raiz (o fluxo mínimo) e o fluxo da assinatura.
	want := append([]uint16{5}, utf16.Encode([]rune("DigitalSignature"))...)
	var rootStart uint32
	var rootSize int64
	found := false
	var sigStart uint32
	var sigSize int64
	sid := firstDir
	idx := 0
	for steps := 0; sid != cfbEnd && !found; steps++ {
		if sid == cfbFree || steps > c.maxSteps || idx > maxDirEnt {
			return nil, errors.New("diretório do MSI ilegível")
		}
		b, err := readAt(c.r, c.size, c.sectorOff(sid), int(c.ssize))
		if err != nil {
			return nil, err
		}
		for o := 0; o+dirEntry <= len(b); o, idx = o+dirEntry, idx+1 {
			e := b[o : o+dirEntry]
			nameLen := int(binary.LittleEndian.Uint16(e[64:]))
			typ := e[66]
			start := binary.LittleEndian.Uint32(e[116:])
			sz := int64(binary.LittleEndian.Uint64(e[120:]))
			if c.shift == 9 {
				sz &= 0xFFFFFFFF // versão 3: só os 32 bits de baixo valem
			}
			if typ == 5 {
				rootStart, rootSize = start, sz
				continue
			}
			if typ != 2 || nameLen < 2 || nameLen > 64 || nameLen%2 != 0 {
				continue
			}
			name := make([]uint16, nameLen/2-1) // sem o 0 do fim
			for k := range name {
				name[k] = binary.LittleEndian.Uint16(e[k*2:])
			}
			if slicesEqual(name, want) {
				found, sigStart, sigSize = true, start, sz
				break
			}
		}
		if !found {
			if sid, err = c.next(sid); err != nil {
				return nil, err
			}
		}
	}
	if !found {
		return nil, ErrUnsigned
	}
	if sigSize <= 0 || sigSize > maxSig {
		return nil, fmt.Errorf("fluxo da assinatura de %d bytes", sigSize)
	}
	if sigSize >= int64(c.miniCutoff) {
		return c.chain(sigStart, sigSize)
	}
	// No fluxo mínimo: setores de 64 bytes dentro do fluxo da raiz, com a cadeia na mini-FAT.
	mini := int64(1) << miniShift
	if mini <= 0 || mini > c.ssize {
		return nil, ErrFormat
	}
	// Só os setores do fluxo da raiz que têm os setores mínimos da assinatura (pela rede, o
	// fluxo mínimo inteiro seriam muitos pedidos).
	rootChain := []uint32{rootStart}
	rootSector := func(i int64) (uint32, error) {
		for int64(len(rootChain)) <= i {
			if len(rootChain) > c.maxSteps {
				return 0, errors.New("fluxo mínimo fora do arquivo")
			}
			n, err := c.next(rootChain[len(rootChain)-1])
			if err != nil {
				return 0, err
			}
			if n == cfbEnd || n == cfbFree {
				return 0, errors.New("setor mínimo fora do fluxo")
			}
			rootChain = append(rootChain, n)
		}
		return rootChain[i], nil
	}
	var out []byte
	ms := sigStart
	for steps := 0; int64(len(out)) < sigSize; steps++ {
		if ms == cfbEnd || ms == cfbFree || steps > int(sigSize/mini)+1 {
			return nil, errors.New("cadeia do fluxo mínimo interrompida")
		}
		off := int64(ms) * mini
		if off+mini > rootSize {
			return nil, errors.New("setor mínimo fora do fluxo")
		}
		sid, err := rootSector(off / c.ssize)
		if err != nil {
			return nil, err
		}
		b, err := readAt(c.r, c.size, c.sectorOff(sid)+off%c.ssize, int(mini))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if ms, err = c.miniNext(ms); err != nil {
			return nil, err
		}
	}
	return out[:sigSize], nil
}

// miniNext: o setor mínimo seguinte, pela mini-FAT (ela mesma uma cadeia de setores comuns).
func (c *cfb) miniNext(ms uint32) (uint32, error) {
	per := uint32(c.ssize / 4)
	sid := c.firstMiniFAT
	if int(ms/per) > c.maxSteps {
		return 0, errors.New("mini-FAT fora do arquivo")
	}
	for k := uint32(0); k < ms/per; k++ {
		var err error
		if sid, err = c.next(sid); err != nil {
			return 0, err
		}
		if sid == cfbEnd || sid == cfbFree {
			return 0, errors.New("mini-FAT incompleta")
		}
	}
	b, err := c.sector(sid)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[(ms%per)*4:]), nil
}

func slicesEqual(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- PKCS#7 ---------------------------------------------------------------------------

var oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type issuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

// signerOf: o certificado do primeiro signatário da SignedData.
func signerOf(der []byte) (Signer, error) {
	var ci contentInfo
	if _, err := asn1.Unmarshal(der, &ci); err != nil {
		return Signer{}, fmt.Errorf("assinatura ilegível: %w", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return Signer{}, errors.New("a assinatura não é PKCS#7 SignedData")
	}
	// SignedData: version, digestAlgorithms, contentInfo, [0] certificates, [1] crls, signerInfos.
	var sd asn1.RawValue
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return Signer{}, fmt.Errorf("SignedData ilegível: %w", err)
	}
	var certs []*x509.Certificate
	var signerInfos []byte
	rest := sd.Bytes
	for len(rest) > 0 {
		var el asn1.RawValue
		var err error
		if rest, err = asn1.Unmarshal(rest, &el); err != nil {
			return Signer{}, fmt.Errorf("SignedData ilegível: %w", err)
		}
		switch {
		case el.Class == asn1.ClassContextSpecific && el.Tag == 0:
			cr := el.Bytes
			for len(cr) > 0 {
				var c asn1.RawValue
				if cr, err = asn1.Unmarshal(cr, &c); err != nil {
					break
				}
				if cert, err := x509.ParseCertificate(c.FullBytes); err == nil {
					certs = append(certs, cert)
				}
			}
		case el.Class == asn1.ClassUniversal && el.Tag == asn1.TagSet:
			signerInfos = el.Bytes // o último SET: os signatários (o primeiro SET é o dos algoritmos)
		}
	}
	if len(certs) == 0 {
		return Signer{}, errors.New("a assinatura não traz certificados")
	}
	cert := certs[0]
	var si asn1.RawValue
	if _, err := asn1.Unmarshal(signerInfos, &si); err == nil {
		var version int
		r, err := asn1.Unmarshal(si.Bytes, &version)
		if err == nil {
			var id asn1.RawValue
			if _, err := asn1.Unmarshal(r, &id); err == nil {
				var is issuerSerial
				if id.Class == asn1.ClassUniversal && id.Tag == asn1.TagSequence {
					if _, err := asn1.Unmarshal(id.FullBytes, &is); err == nil {
						for _, c := range certs {
							if bytes.Equal(c.RawIssuer, is.Issuer.FullBytes) && c.SerialNumber.Cmp(is.Serial) == 0 {
								cert = c
							}
						}
					}
				} else if id.Class == asn1.ClassContextSpecific && id.Tag == 0 {
					for _, c := range certs {
						if bytes.Equal(c.SubjectKeyId, id.Bytes) {
							cert = c
						}
					}
				}
			}
		}
	}
	s := Signer{CommonName: cert.Subject.CommonName, Subject: cert.Subject.String()}
	if len(cert.Subject.Organization) > 0 {
		s.Organization = cert.Subject.Organization[0]
	}
	return s, nil
}
