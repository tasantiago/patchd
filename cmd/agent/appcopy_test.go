package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func comprime(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A Central embutida sai descomprimida e conferida pelo SHA-256 do build; sem ela, ou com outro
// conteúdo, nada é gravado.
func TestCentralEmbutida(t *testing.T) {
	exe := []byte("MZ" + strings.Repeat("central", 1000))
	sum := sha256.Sum256(exe)
	img, err := userAppImage(comprime(t, exe), hex.EncodeToString(sum[:])+"\n")
	if err != nil || !bytes.Equal(img, exe) {
		t.Fatalf("Central: %v", err)
	}
	if _, err := userAppImage(nil, ""); !errors.Is(err, errNoEmbeddedApp) {
		t.Errorf("sem a Central: %v", err)
	}
	if _, err := userAppImage(comprime(t, exe), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Errorf("SHA-256 diferente: %v", err)
	}
	if _, err := userAppImage([]byte("não é gzip"), hex.EncodeToString(sum[:])); err == nil {
		t.Error("conteúdo ilegível aceito")
	}
}

// A gravação: igual não mexe; diferente tira a antiga do caminho (a em uso numa sessão só pode
// ser renomeada) e põe a nova, byte a byte; as antigas que ninguém usa saem na vez seguinte.
func TestGravarCentral(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "patchd-app.exe")
	v1, v2 := []byte("MZ versão 1"), []byte("MZ versão 2")
	if mudou, err := writeUserApp(dest, v1); err != nil || !mudou {
		t.Fatalf("primeira: %v %v", mudou, err)
	}
	if mudou, err := writeUserApp(dest, v1); err != nil || mudou {
		t.Errorf("igual: %v %v", mudou, err)
	}
	if mudou, err := writeUserApp(dest, v2); err != nil || !mudou {
		t.Fatalf("nova: %v %v", mudou, err)
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, v2) {
		t.Errorf("conteúdo: %q", b)
	}
	if old, _ := filepath.Glob(dest + ".old-*"); len(old) != 1 {
		t.Errorf("a antiga renomeada: %v", old)
	}
	if _, err := writeUserApp(dest, v2); err != nil {
		t.Fatal(err)
	}
	if old, _ := filepath.Glob(dest + ".old-*"); len(old) != 0 {
		t.Errorf("antigas que ficaram: %v", old)
	}
	if _, err := os.Stat(dest + ".novo"); !os.IsNotExist(err) {
		t.Error("sobrou o .novo")
	}
}
