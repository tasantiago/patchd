// Package ber codifica e decodifica o subconjunto do BER (ITU-T X.690) que o LDAP usa
// (RFC 4511, seção 5.1): comprimento sempre definido, número de tag menor que 31.
//
// É o suficiente para o login pelo AD (Aula 7.3) sem uma biblioteca LDAP inteira, que
// traria NTLM, Kerberos e golang.org/x/crypto para três operações (bind, search, unbind).
package ber

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Classes do identificador (bits 8 e 7 do primeiro octeto).
const (
	ClassUniversal   byte = 0x00
	ClassApplication byte = 0x40
	ClassContext     byte = 0x80
	Constructed      byte = 0x20
)

// Tags universais usadas pelo LDAP.
const (
	TagBoolean     byte = 0x01
	TagInteger     byte = 0x02
	TagOctetString byte = 0x04
	TagNull        byte = 0x05
	TagEnumerated  byte = 0x0a
	TagSequence    byte = 0x30 // SEQUENCE já com o bit de construído
	TagSet         byte = 0x31
)

// MaxMessage limita o tamanho de uma mensagem lida da rede. As respostas do login têm
// poucas centenas de bytes; o limite impede que um servidor (ou alguém no meio) faça o
// processo alocar gigabytes com um comprimento forjado.
const MaxMessage = 1 << 20

// maxDepth limita o aninhamento ao decodificar (cada filtro AND é um nível).
const maxDepth = 32

// Packet é um elemento decodificado: o identificador inteiro (classe, construído e tag
// num octeto só), o conteúdo e, se construído, os filhos.
type Packet struct {
	ID       byte
	Value    []byte
	Children []*Packet
}

// Class devolve a classe do identificador.
func (p *Packet) Class() byte { return p.ID & 0xc0 }

// Tag devolve o número da tag.
func (p *Packet) Tag() byte { return p.ID & 0x1f }

// IsConstructed diz se o elemento tem filhos.
func (p *Packet) IsConstructed() bool { return p.ID&Constructed != 0 }

// String devolve o conteúdo como texto (OCTET STRING).
func (p *Packet) String() string { return string(p.Value) }

// Int devolve o conteúdo como inteiro com sinal (INTEGER, ENUMERATED).
func (p *Packet) Int() (int64, error) { return decodeInt(p.Value) }

// Bool devolve o conteúdo como BOOLEAN.
func (p *Packet) Bool() (bool, error) {
	if len(p.Value) != 1 {
		return false, errors.New("ber: BOOLEAN com tamanho diferente de 1")
	}
	return p.Value[0] != 0, nil
}

// TLV monta um elemento: identificador, comprimento e conteúdo.
func TLV(id byte, content []byte) []byte {
	out := make([]byte, 0, len(content)+6)
	out = append(out, id)
	out = appendLength(out, len(content))
	return append(out, content...)
}

// Cons monta um elemento construído com os filhos já codificados.
func Cons(id byte, children ...[]byte) []byte {
	n := 0
	for _, c := range children {
		n += len(c)
	}
	content := make([]byte, 0, n)
	for _, c := range children {
		content = append(content, c...)
	}
	return TLV(id|Constructed, content)
}

// Str monta um OCTET STRING (ou outra tag primitiva com conteúdo textual).
func Str(id byte, s string) []byte { return TLV(id, []byte(s)) }

// Int monta um INTEGER/ENUMERATED na forma mínima em complemento de dois.
func Int(id byte, v int64) []byte { return TLV(id, encodeInt(v)) }

// Bool monta um BOOLEAN (verdadeiro = 0xff, como o DER).
func Bool(id byte, v bool) []byte {
	if v {
		return TLV(id, []byte{0xff})
	}
	return TLV(id, []byte{0x00})
}

func appendLength(out []byte, n int) []byte {
	if n < 0x80 {
		return append(out, byte(n))
	}
	var tmp [8]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte(n)
		n >>= 8
	}
	out = append(out, 0x80|byte(len(tmp)-i))
	return append(out, tmp[i:]...)
}

func encodeInt(v int64) []byte {
	b := []byte{byte(v)}
	for v >= 0x80 || v < -0x80 {
		v >>= 8
		b = append([]byte{byte(v)}, b...)
	}
	return b
}

func decodeInt(b []byte) (int64, error) {
	if len(b) == 0 || len(b) > 8 {
		return 0, fmt.Errorf("ber: inteiro com %d bytes", len(b))
	}
	v := int64(int8(b[0])) // o primeiro byte carrega o sinal
	for _, c := range b[1:] {
		v = v<<8 | int64(c)
	}
	return v, nil
}

// ReadMessage lê um elemento completo (a mensagem LDAP) do fluxo, sem decodificá-lo.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	id, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if id&0x1f == 0x1f {
		return nil, errors.New("ber: tag de número alto não é usada pelo LDAP")
	}
	first, err := r.ReadByte()
	if err != nil {
		return nil, unexpected(err)
	}
	header := []byte{id, first}
	n := int(first)
	if first&0x80 != 0 {
		k := int(first & 0x7f)
		if k == 0 {
			return nil, errors.New("ber: comprimento indefinido não é permitido no LDAP")
		}
		if k > 4 {
			return nil, errors.New("ber: comprimento grande demais")
		}
		n = 0
		for i := 0; i < k; i++ {
			c, err := r.ReadByte()
			if err != nil {
				return nil, unexpected(err)
			}
			header = append(header, c)
			n = n<<8 | int(c)
		}
	}
	if n > MaxMessage {
		return nil, fmt.Errorf("ber: mensagem de %d bytes acima do limite", n)
	}
	msg := make([]byte, len(header)+n)
	copy(msg, header)
	if _, err := io.ReadFull(r, msg[len(header):]); err != nil {
		return nil, unexpected(err)
	}
	return msg, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Parse decodifica exatamente um elemento; bytes sobrando são erro.
func Parse(b []byte) (*Packet, error) {
	p, rest, err := parse(b, 0)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("ber: %d bytes depois do elemento", len(rest))
	}
	return p, nil
}

func parse(b []byte, depth int) (*Packet, []byte, error) {
	if depth > maxDepth {
		return nil, nil, errors.New("ber: aninhamento profundo demais")
	}
	if len(b) < 2 {
		return nil, nil, errors.New("ber: elemento truncado")
	}
	p := &Packet{ID: b[0]}
	if p.ID&0x1f == 0x1f {
		return nil, nil, errors.New("ber: tag de número alto não é usada pelo LDAP")
	}
	n, off := int(b[1]), 2
	if b[1]&0x80 != 0 {
		k := int(b[1] & 0x7f)
		if k == 0 || k > 4 || len(b) < 2+k {
			return nil, nil, errors.New("ber: comprimento inválido")
		}
		n = 0
		for _, c := range b[2 : 2+k] {
			n = n<<8 | int(c)
		}
		off += k
	}
	if n > len(b)-off {
		return nil, nil, errors.New("ber: elemento truncado")
	}
	p.Value = b[off : off+n]
	rest := b[off+n:]
	if p.IsConstructed() {
		inner := p.Value
		for len(inner) > 0 {
			c, r, err := parse(inner, depth+1)
			if err != nil {
				return nil, nil, err
			}
			p.Children = append(p.Children, c)
			inner = r
		}
	}
	return p, rest, nil
}
