package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestAppNamer(t *testing.T) {
	dir := t.TempDir()
	if n := loadAppName(dir); n != protocol.DefaultAppName {
		t.Fatalf("sem arquivo: %q", n)
	}
	var aplicados []string
	var log bytes.Buffer
	n := &appNamer{dir: dir, logger: slog.New(slog.NewTextHandler(&log, nil)),
		apply: func(name string) (bool, error) { aplicados = append(aplicados, name); return true, nil }}
	n.Set(protocol.DefaultAppName) // igual ao padrão: nada
	n.Set("Atualizações da TI")
	n.Set("Atualizações da TI") // repetido: nada
	n.Set("a\x00b")             // inválido: nada
	if len(aplicados) != 1 || aplicados[0] != "Atualizações da TI" {
		t.Errorf("aplicados: %q", aplicados)
	}
	if got := loadAppName(dir); got != "Atualizações da TI" {
		t.Errorf("guardado: %q", got)
	}
	// O serviço reinicia: lê o guardado, e o mesmo nome não refaz nada.
	n2 := &appNamer{dir: dir, logger: n.logger, apply: n.apply}
	n2.Set("Atualizações da TI")
	if len(aplicados) != 1 {
		t.Errorf("depois de reiniciar: %q", aplicados)
	}
	// Arquivo adulterado: vale o padrão.
	_ = os.WriteFile(filepath.Join(dir, appNameFile), []byte("x\x1b[31m"), 0o644)
	if got := loadAppName(dir); got != protocol.DefaultAppName {
		t.Errorf("adulterado: %q", got)
	}
}
