package jobs

import (
	"crypto/ed25519"
	"encoding/base64"
)

// signBytes assina bytes arbitrários (só nos testes: simula um servidor de outra versão).
func signBytes(priv []byte, payload []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(priv), append([]byte(signingContext), payload...))), nil
}
