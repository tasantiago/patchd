package ber

import (
	"bufio"
	"bytes"
	"testing"
)

// Aula 20.3: o BER chega do servidor LDAP (o AD, por LDAPS). Para qualquer sequência de
// bytes, Parse não pode entrar em pânico; o que ele aceita, o codificador do pacote remonta
// igual, e o ReadMessage lê do fluxo exatamente o mesmo elemento.
func FuzzParse(f *testing.F) {
	f.Add(Cons(TagSequence, Int(TagInteger, 1), Cons(ClassApplication|Constructed|1,
		Int(TagEnumerated, 0), Str(TagOctetString, ""), Str(TagOctetString, "ok"))))
	f.Add([]byte{0x30, 0x84, 0, 0, 0, 3, 0x02, 0x01, 0x05}) // comprimento longo em 4 bytes
	f.Add(Int(TagInteger, -129))
	f.Add(Bool(TagBoolean, true))
	f.Add([]byte{0x04, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Parse(b)
		if err != nil {
			return
		}
		walk(p)
		q, err := Parse(encode(p))
		if err != nil {
			t.Fatalf("remontado não decodifica: %v", err)
		}
		if !same(p, q) {
			t.Fatal("remontado decodifica diferente")
		}
		raw, err := ReadMessage(bufio.NewReader(bytes.NewReader(b)))
		if err != nil || !bytes.Equal(raw, b) {
			t.Fatalf("ReadMessage leu outra coisa: %v", err)
		}
	})
}

// walk chama os leitores de valor em todos os nós (não podem entrar em pânico).
func walk(p *Packet) {
	_, _ = p.Int()
	_, _ = p.Bool()
	_ = p.String()
	for _, c := range p.Children {
		walk(c)
	}
}

func encode(p *Packet) []byte {
	if !p.IsConstructed() {
		return TLV(p.ID, p.Value)
	}
	var ch [][]byte
	for _, c := range p.Children {
		ch = append(ch, encode(c))
	}
	return Cons(p.ID, ch...)
}

func same(a, b *Packet) bool {
	if a.ID != b.ID || len(a.Children) != len(b.Children) {
		return false
	}
	if !a.IsConstructed() {
		return bytes.Equal(a.Value, b.Value)
	}
	for i := range a.Children {
		if !same(a.Children[i], b.Children[i]) {
			return false
		}
	}
	return true
}
