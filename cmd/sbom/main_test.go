package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/sbom"
)

// O executável deste teste é um executável Go (sem commit gravado: o go test não grava).
func TestSBOM(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "patchd-agent-linux-amd64")
	data, _ := os.ReadFile(self)
	_ = os.WriteFile(exe, data, 0o755)
	out := filepath.Join(dir, "sbom")
	var o, e bytes.Buffer
	if code := run([]string{"-out", out, "-version", "v0.17.11", exe}, &o, &e); code != exitOK {
		t.Fatalf("%d %s", code, e.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "patchd-agent-linux-amd64.cdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b sbom.BOM
	if err := json.Unmarshal(raw, &b); err != nil || b.Metadata.Component.Version != "v0.17.11" ||
		b.Metadata.Component.Hashes[0].Content != sbom.Sum(data) {
		t.Errorf("%v %+v", err, b.Metadata.Component)
	}
	resumo, _ := os.ReadFile(filepath.Join(out, "modulos.txt"))
	if !strings.Contains(string(resumo), "stdlib") || !strings.Contains(string(resumo), "todos") {
		t.Errorf("resumo:\n%s", resumo)
	}
	if !strings.Contains(o.String(), "ok  patchd-agent-linux-amd64.cdx.json") {
		t.Errorf("saída: %s", o.String())
	}

	// De outro commit (este não tem commit gravado): recusado.
	e.Reset()
	if code := run([]string{"-out", out, "-revision", "8ba97055b6ca", exe}, &o, &e); code != exitFail || !strings.Contains(e.String(), "não do 8ba97055b6ca") {
		t.Errorf("revisão: %d %s", code, e.String())
	}
	// Não é Go: recusado.
	txt := filepath.Join(dir, "instalar.cmd")
	_ = os.WriteFile(txt, []byte("@echo off"), 0o644)
	e.Reset()
	if code := run([]string{"-out", out, txt}, &o, &e); code != exitFail || !strings.Contains(e.String(), "não é um executável Go") {
		t.Errorf("não Go: %d %s", code, e.String())
	}
	if code := run([]string{exe}, &o, &e); code != exitConfig {
		t.Errorf("sem -out: %d", code)
	}
}
