package ber

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestInteiros(t *testing.T) {
	for v, want := range map[int64]string{
		0: "020100", 3: "020103", 127: "02017f", 128: "02020080", 255: "020200ff", 256: "02020100",
		-1: "0201ff", -128: "020180", -129: "0202ff7f", 2147483647: "02047fffffff",
	} {
		got := Int(TagInteger, v)
		if hex.EncodeToString(got) != want {
			t.Errorf("%d: %x, esperado %s", v, got, want)
		}
		p, err := Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if back, err := p.Int(); err != nil || back != v {
			t.Errorf("%d: volta %d %v", v, back, err)
		}
	}
}

func TestComprimentoLongoEAninhamento(t *testing.T) {
	longo := strings.Repeat("x", 300)
	msg := Cons(TagSequence, Int(TagInteger, 7), Str(TagOctetString, longo), Bool(TagBoolean, true))
	// 300 bytes: forma longa com 2 octetos (0x82 0x01 0x2c).
	if !bytes.Contains(msg, []byte{0x04, 0x82, 0x01, 0x2c}) {
		t.Errorf("comprimento longo: %x", msg[:12])
	}
	p, err := Parse(msg)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != TagSequence || len(p.Children) != 3 || p.Children[1].String() != longo {
		t.Fatalf("%+v", p)
	}
	if b, _ := p.Children[2].Bool(); !b {
		t.Error("bool")
	}
	// ReadMessage lê a mensagem inteira do fluxo e para nela.
	r := bufio.NewReader(bytes.NewReader(append(msg, 0x30, 0x00)))
	got, err := ReadMessage(r)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got, err := ReadMessage(r); err != nil || !bytes.Equal(got, []byte{0x30, 0x00}) {
		t.Errorf("segunda mensagem: %x %v", got, err)
	}
}

func TestEntradasRuins(t *testing.T) {
	for nome, b := range map[string]string{
		"truncado":          "3005020101",
		"indefinido":        "308002010100 00",
		"comprimento 5 oct": "3085000000000102",
		"tag alta":          "1f0100",
		"sobra":             "0201000000",
		"filho truncado":    "30030201",
		"inteiro vazio":     "",
	} {
		b = strings.ReplaceAll(b, " ", "")
		raw, _ := hex.DecodeString(b)
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: deveria falhar", nome)
		}
	}
	if _, err := (&Packet{}).Int(); err == nil {
		t.Error("inteiro sem bytes")
	}
	// Aninhamento abusivo.
	deep := []byte{0x04, 0x00}
	for i := 0; i < 40; i++ {
		deep = Cons(TagSequence, deep)
	}
	if _, err := Parse(deep); err == nil || !strings.Contains(err.Error(), "profundo") {
		t.Errorf("aninhamento: %v", err)
	}
	// Mensagem acima do limite: recusada antes de alocar.
	r := bufio.NewReader(bytes.NewReader([]byte{0x30, 0x84, 0x7f, 0xff, 0xff, 0xff}))
	if _, err := ReadMessage(r); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite: %v", err)
	}
	r = bufio.NewReader(bytes.NewReader([]byte{0x30, 0x05, 0x02}))
	if _, err := ReadMessage(r); err == nil {
		t.Error("fluxo truncado")
	}
}
