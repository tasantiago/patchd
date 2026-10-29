package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/tasantiago/patchd/internal/machinebox"
)

func TestDiscoveryKeygen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovery.key")
	var out, log bytes.Buffer
	if code := discoveryKeygen([]string{"-out", path}, &out, &log); code != exitOK {
		t.Fatalf("keygen: %d", code)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machinebox.ParsePrivate(string(before)); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("key file permissions")
	}
	if bytes.Contains(out.Bytes(), bytes.TrimSpace(before)) {
		t.Fatal("key leaked in output")
	}
	if code := discoveryKeygen([]string{"-out", path}, &out, &log); code == exitOK {
		t.Fatal("overwrote key")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("changed existing key")
	}
}
