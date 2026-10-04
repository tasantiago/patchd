package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func manifestoDeTeste(conteudo []byte) Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Version:       "v0.5.1",
		CreatedAt:     time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Files: []File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
			SHA256: HashBytes(conteudo), Size: int64(len(conteudo))}},
	}
}

func TestAssinaEConfere(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data, sig, err := Sign(priv, manifestoDeTeste([]byte("agente")))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Verify(pub, data, sig)
	if err != nil || m.Version != "v0.5.1" {
		t.Fatalf("manifesto íntegro: %+v %v", m, err)
	}
	if f, ok := m.For("linux", "amd64"); !ok || f.Name != "patchd-agent-linux-amd64" {
		t.Errorf("For: %+v %t", f, ok)
	}

	// Um byte mudado no manifesto (por exemplo, o SHA-256 de outro executável): recusado.
	alterado := []byte(strings.Replace(string(data), "v0.5.1", "v0.5.9", 1))
	if _, err := Verify(pub, alterado, sig); err == nil || !strings.Contains(err.Error(), "inválida") {
		t.Errorf("manifesto alterado: %v", err)
	}
	// Outra chave: recusado.
	outra, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(outra, data, sig); err == nil {
		t.Error("chave errada deveria ser recusada")
	}
	// Assinatura dos mesmos bytes sem o contexto de manifesto: recusada.
	semContexto := ed25519.Sign(priv, data)
	if _, err := Verify(pub, data, []byte(base64.StdEncoding.EncodeToString(semContexto))); err == nil {
		t.Error("assinatura sem o contexto deveria ser recusada")
	}
	if _, err := Verify(pub, data, []byte("lixo")); err == nil {
		t.Error("assinatura ilegível deveria ser recusada")
	}
}

func TestChaves(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p2, err := ParsePrivateKey(EncodePrivateKey(priv))
	if err != nil || !p2.Equal(priv) {
		t.Fatalf("chave privada: %v", err)
	}
	pub2, err := ParsePublicKey(EncodePublicKey(pub))
	if err != nil || !pub2.Equal(pub) {
		t.Fatalf("chave pública: %v", err)
	}
	for _, ruim := range []string{"", "abc", EncodePublicKey(pub)[:20]} {
		if _, err := ParsePublicKey(ruim); err == nil {
			t.Errorf("chave pública %q deveria ser recusada", ruim)
		}
	}
	if _, err := ParsePrivateKey(EncodePublicKey(pub)); err == nil {
		t.Error("chave pública não serve como privada")
	}
}

func TestDirPublica(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := Dir(t.TempDir())
	if v, err := d.Current(); err != nil || v != "" {
		t.Fatalf("sem publicação: %q %v", v, err)
	}

	conteudo := []byte("agente v0.5.1")
	vdir := filepath.Join(string(d), "v0.5.1")
	_ = os.MkdirAll(vdir, 0o755)
	data, sig, _ := Sign(priv, manifestoDeTeste(conteudo))
	_ = os.WriteFile(filepath.Join(vdir, ManifestFile), data, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, SignatureFile), sig, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)

	if err := d.Publish("v0.5.1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Current(); v != "v0.5.1" {
		t.Errorf("publicada: %q", v)
	}

	// Executável trocado depois de assinado: a publicação recusa.
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), []byte("outro"), 0o644)
	if err := d.Publish("v0.5.1"); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Errorf("executável trocado: %v", err)
	}
	if err := d.Publish("v9.9.9"); err == nil {
		t.Error("versão inexistente deveria ser recusada")
	}

	// Nomes perigosos nunca abrem nada.
	for _, c := range [][2]string{{"..", "x"}, {"v0.5.1", "../current"}, {"v0.5.1", ".oculto"}, {"v0.5.1/..", ManifestFile}} {
		if f, err := d.Open(c[0], c[1]); err == nil {
			f.Close()
			t.Errorf("Open(%q, %q) deveria falhar", c[0], c[1])
		}
	}
	if f, err := d.Open("v0.5.1", ManifestFile); err != nil {
		t.Errorf("manifesto: %v", err)
	} else {
		f.Close()
	}

	if err := d.Withdraw(); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Current(); v != "" {
		t.Errorf("retirada: %q", v)
	}
}
