package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWinres(t *testing.T) {
	dir := t.TempDir()
	var o, e bytes.Buffer
	if code := run([]string{"-version", "v0.17.13", "-name", "Central de software", "-out", dir}, &o, &e); code != 0 {
		t.Fatalf("%d %s", code, e.String())
	}
	for _, a := range []string{"amd64", "arm64"} {
		if fi, err := os.Stat(filepath.Join(dir, "versao_windows_"+a+".syso")); err != nil || fi.Size() < 200 {
			t.Errorf("%s: %v", a, err)
		}
	}
	for _, args := range [][]string{{"-out", dir}, {"-version", "v1.0.0", "-name", "", "-out", dir}, {"-version", "v1.0.0", "-name", "a\tb", "-out", dir}} {
		if code := run(args, &o, &e); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}
