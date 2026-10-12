package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/credential"
)

func caDeTeste(t *testing.T) []byte {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA de teste"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestCAFile: o -ca-file aceita só certificados (Aula 9.2) e vira o transporte do agente.
func TestCAFile(t *testing.T) {
	dir := t.TempDir()
	escreve := func(nome string, data []byte) string {
		p := filepath.Join(dir, nome)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ca := caDeTeste(t)
	rt, n, err := agentTransport(escreve("ca.crt", ca))
	if err != nil || n != 1 {
		t.Fatalf("ca.crt: %d %v", n, err)
	}
	if tr, ok := rt.(*http.Transport); !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Errorf("transporte sem as CAs do arquivo: %#v", rt)
	}
	if _, n, err := agentTransport(escreve("duas.crt", append(ca, caDeTeste(t)...))); err != nil || n != 2 {
		t.Errorf("duas CAs: %d %v", n, err)
	}
	if rt, _, err := agentTransport(""); rt != nil || err != nil {
		t.Errorf("sem -ca-file: as CAs do sistema (nil): %v %v", rt, err)
	}
	for nome, data := range map[string][]byte{
		"vazio.crt":     []byte("nada aqui\n"),
		"com-chave.pem": append(ca, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}})...),
	} {
		if _, _, err := agentTransport(escreve(nome, data)); err == nil {
			t.Errorf("%s aceito", nome)
		}
	}
	if _, _, err := agentTransport(filepath.Join(dir, "nao-existe.crt")); err == nil {
		t.Error("arquivo inexistente aceito")
	}
	// Caminho relativo: recusado na configuração.
	if _, err := loadAgentConfig([]string{"-ca-file", "ca.crt"}, func(string) (string, bool) { return "", false }, &strings.Builder{}); err == nil ||
		!strings.Contains(err.Error(), "absoluto") {
		t.Errorf("caminho relativo: %v", err)
	}
}

// TestCredencialEnderecoAnterior: depois da troca, o install com o endereço antigo (script
// de boot) continua reconhecendo a máquina (Aula 9.2).
func TestCredencialEnderecoAnterior(t *testing.T) {
	c := credential.Credential{ServerURL: "https://patchd.exemplo:8443", PreviousServerURL: "http://127.0.0.1:18080"}
	for url, quer := range map[string]bool{
		"https://patchd.exemplo:8443/": true, "HTTPS://PATCHD.exemplo:8443": true, "http://127.0.0.1:18080": true,
		"http://127.0.0.1:18081": false, "": false,
	} {
		if c.Matches(url) != quer {
			t.Errorf("%q: %v", url, !quer)
		}
	}
	if (credential.Credential{ServerURL: "http://127.0.0.1:18080"}).Matches("") {
		t.Error("vazio não confere")
	}
}
