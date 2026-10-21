package machinebox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCaixa(t *testing.T) {
	priv, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := Public(priv)
	if err != nil || len(pub) != PublicKeySize {
		t.Fatalf("pública: %d %v", len(pub), err)
	}
	segredo := []byte("S3nh@-da-BIOS-de-teste")
	sealed, err := Seal(pub, PurposeBIOS, segredo)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, segredo) {
		t.Fatal("o segredo aparece na caixa")
	}
	got, err := Open(priv, PurposeBIOS, sealed)
	if err != nil || !bytes.Equal(got, segredo) {
		t.Fatalf("abrir: %q %v", got, err)
	}
	// Duas caixas do mesmo segredo são diferentes (o HPKE sorteia a cada vez).
	if s2, _ := Seal(pub, PurposeBIOS, segredo); bytes.Equal(s2, sealed) {
		t.Error("caixas iguais para o mesmo segredo")
	}

	outra, _ := Generate()
	for nome, f := range map[string]func() error{
		"outra máquina": func() error { _, err := Open(outra, PurposeBIOS, sealed); return err },
		"outro uso":     func() error { _, err := Open(priv, "outra-coisa", sealed); return err },
		"alterada": func() error {
			x := bytes.Clone(sealed)
			x[len(x)-1] ^= 1
			_, err := Open(priv, PurposeBIOS, x)
			return err
		},
	} {
		if err := f(); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	if _, err := Seal(pub, "Uso Inválido!", segredo); err == nil {
		t.Error("rótulo inválido aceito")
	}
	if _, err := Seal(pub, PurposeBIOS, make([]byte, MaxPlaintext+1)); err == nil {
		t.Error("segredo grande demais aceito")
	}
	if _, err := Seal(pub[:10], PurposeBIOS, segredo); err == nil {
		t.Error("chave pública curta aceita")
	}
}

func TestFormatos(t *testing.T) {
	priv, _ := Generate()
	pub, _ := Public(priv)
	p2, err := ParsePrivate(EncodePrivate(priv))
	if err != nil || !bytes.Equal(p2, priv) {
		t.Fatalf("privada: %v", err)
	}
	q, err := ParsePublic(EncodePublic(pub))
	if err != nil || !bytes.Equal(q, pub) {
		t.Fatalf("pública: %v", err)
	}
	for _, ruim := range []string{"", "patchd_mbox_", "patchd_mbox_###", "outro_" + strings.TrimPrefix(EncodePrivate(priv), "patchd_mbox_")} {
		if _, err := ParsePrivate(ruim); err == nil {
			t.Errorf("privada %q aceita", ruim)
		}
	}
	for _, ruim := range []string{"", "AAAA", EncodePublic(pub[:len(pub)-1])} {
		if _, err := ParsePublic(ruim); err == nil {
			t.Errorf("pública %q aceita", ruim)
		}
	}
	if id := KeyID(pub); len(id) != 12 {
		t.Errorf("KeyID %q", id)
	}
}
