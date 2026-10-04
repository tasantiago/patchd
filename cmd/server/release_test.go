package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/release"
)

func TestReleasePublicaSoAgenteNaoMaisNovoQueOServidor(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	for _, v := range []string{"v0.5.0", "v0.5.1"} {
		conteudo := []byte("agente " + v)
		vdir := filepath.Join(dir, v)
		_ = os.MkdirAll(vdir, 0o755)
		data, sig, err := release.Sign(priv, release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: v,
			CreatedAt: time.Now().UTC(), Files: []release.File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
				SHA256: release.HashBytes(conteudo), Size: int64(len(conteudo))}}})
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(vdir, release.ManifestFile), data, 0o644)
		_ = os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644)
		_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)
	}
	amb := func(k string) (string, bool) {
		if k == "PATCHD_RELEASES_DIR" {
			return dir, true
		}
		return "", false
	}

	var out, errs bytes.Buffer
	// Servidor v0.5.0: o agente v0.5.1 é recusado; o v0.5.0, aceito.
	if code := runRelease([]string{"publish", "v0.5.1"}, amb, &out, &errs, "v0.5.0"); code != exitConfig || !strings.Contains(errs.String(), "atualize o servidor antes") {
		t.Errorf("agente mais novo que o servidor: %d %s", code, errs.String())
	}
	if code := runRelease([]string{"publish", "v0.5.0"}, amb, &out, &errs, "v0.5.0"); code != exitOK {
		t.Fatalf("publicar v0.5.0: %d %s", code, errs.String())
	}
	// Servidor "dev" (sem versão de verdade): nada é publicado.
	errs.Reset()
	if code := runRelease([]string{"publish", "v0.5.0"}, amb, &out, &errs, "dev"); code != exitConfig {
		t.Errorf("servidor sem versão: %d %s", code, errs.String())
	}

	out.Reset()
	if code := runRelease([]string{"current"}, amb, &out, &errs, "v0.5.0"); code != exitOK || strings.TrimSpace(out.String()) != "v0.5.0" {
		t.Errorf("current: %d %q", code, out.String())
	}
	if code := runRelease([]string{"withdraw"}, amb, &out, &errs, "v0.5.0"); code != exitOK {
		t.Errorf("withdraw: %d", code)
	}
	if v, _ := release.Dir(dir).Current(); v != "" {
		t.Errorf("depois do withdraw: %q", v)
	}
}
