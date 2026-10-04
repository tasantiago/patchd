package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/release"
)

func TestKeygenEAssinatura(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "chaves", "release.key")
	var out, errs bytes.Buffer
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errs.String())
	}
	pub, err := release.ParsePublicKey(out.String())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissão da chave: %s", fi.Mode().Perm())
	}
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code == exitOK {
		t.Error("keygen não pode sobrescrever uma chave existente")
	}

	dist := filepath.Join(dir, "dist")
	_ = os.MkdirAll(dist, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-linux-amd64"), []byte("agente linux"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-windows-amd64.exe"), []byte("agente windows"), 0o755)
	rel := filepath.Join(dir, "releases")

	out.Reset()
	args := []string{"sign", "-key", key, "-version", "v0.5.1", "-dist", dist, "-out", rel}
	if code := run(args, &out, &errs); code != exitOK || !strings.Contains(out.String(), "2 executáveis") {
		t.Fatalf("sign: %d %s %s", code, out.String(), errs.String())
	}
	data, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.ManifestFile))
	sig, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.SignatureFile))
	m, err := release.Verify(pub, data, sig)
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("o manifesto assinado confere com a chave pública: %+v %v", m, err)
	}
	if _, err := release.Dir(rel).Check("v0.5.1"); err != nil {
		t.Errorf("a pasta gerada passa na conferência do servidor: %v", err)
	}

	// A mesma versão não é assinada de novo.
	if code := run(args, &out, &errs); code == exitOK {
		t.Error("versão já assinada deveria ser recusada")
	}

	// Chave legível por outros: recusada.
	_ = os.Chmod(key, 0o644)
	args[6] = "v0.5.2"
	errs.Reset()
	if code := run(args, &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "chmod 600") {
		t.Errorf("chave aberta: %d %s", code, errs.String())
	}
}
