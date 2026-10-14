package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// PATCHD_AGENT_CA_FILE (Aula 15.2): só certificados; a chave privada nunca vira download.
func TestLoadAgentCA(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA de teste"}, NotBefore: time.Now(),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	escreve := func(nome string, data []byte) string {
		p := filepath.Join(dir, nome)
		os.WriteFile(p, data, 0o644)
		return p
	}
	if data, n, err := loadAgentCA(escreve("ca.crt", append(cert, cert...))); err != nil || n != 2 || len(data) == 0 {
		t.Errorf("duas: %d %v", n, err)
	}
	for nome, c := range map[string]struct {
		path, msg string
	}{
		"com a chave": {escreve("par.pem", append(cert, key...)), `bloco "PRIVATE KEY"`},
		"vazio":       {escreve("vazio.pem", []byte("nada")), "nenhum certificado"},
		"relativo":    {"ca.crt", "absoluto"},
		"ilegível":    {escreve("ruim.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})), "ilegível"},
	} {
		if _, _, err := loadAgentCA(c.path); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
}
