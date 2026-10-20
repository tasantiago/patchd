package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"regexp"
	"testing"
)

// TestVerifyAnyRelease (Aula 20.7, S8): manifesto assinado pela chave atual ou pela
// seguinte é aceito; um manifesto alterado é recusado com qualquer chave.
func TestVerifyAnyRelease(t *testing.T) {
	atual, privAtual, _ := ed25519.GenerateKey(rand.Reader)
	seguinte, privSeguinte, _ := ed25519.GenerateKey(rand.Reader)
	_, privOutra, _ := ed25519.GenerateKey(rand.Reader)
	chaves := []ed25519.PublicKey{atual, seguinte}
	for nome, priv := range map[string]ed25519.PrivateKey{"atual": privAtual, "seguinte": privSeguinte} {
		data, sig, _ := Sign(priv, manifestoDeTeste([]byte("agente")))
		if m, err := VerifyAny(chaves, data, sig); err != nil || m.Version != "v0.5.1" {
			t.Errorf("%s: %+v %v", nome, m, err)
		}
	}
	data, sig, _ := Sign(privOutra, manifestoDeTeste([]byte("agente")))
	if _, err := VerifyAny(chaves, data, sig); !errors.Is(err, ErrSignature) {
		t.Errorf("outra chave: %v", err)
	}
	if _, err := VerifyAny(nil, data, sig); err == nil {
		t.Error("sem chaves deveria recusar")
	}
	if id := KeyID(atual); !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) || id == KeyID(seguinte) {
		t.Errorf("KeyID: %q", id)
	}
}
