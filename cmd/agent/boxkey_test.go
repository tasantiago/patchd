package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/machinebox"
)

func TestChaveDaCaixa(t *testing.T) {
	if credential.BoxKeyFile != machinebox.FileName {
		t.Fatal("o nome do arquivo da chave diverge entre credential e machinebox")
	}
	dir := t.TempDir()
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	pub := boxPublic(logger, dir)
	if pub == "" || !strings.Contains(log.String(), "chave da caixa criada") {
		t.Fatalf("criar: %q\n%s", pub, log.String())
	}
	// Mesma chave na próxima partida.
	if again := boxPublic(logger, dir); again != pub {
		t.Error("a chave mudou entre partidas")
	}
	// O que o servidor fechar com a pública, o agente abre com a privada guardada.
	raw, _ := machinebox.ParsePublic(pub)
	sealed, _ := machinebox.Seal(raw, machinebox.PurposeBIOS, []byte("segredo"))
	priv, _ := loadBoxKey(logger, dir)
	if got, err := machinebox.Open(priv, machinebox.PurposeBIOS, sealed); err != nil || string(got) != "segredo" {
		t.Errorf("abrir: %q %v", got, err)
	}
	// Arquivo estragado: guardado à parte e trocado.
	_ = os.WriteFile(filepath.Join(dir, machinebox.FileName), []byte("lixo"), 0o600)
	log.Reset()
	novo := boxPublic(logger, dir)
	ruins, _ := filepath.Glob(filepath.Join(dir, machinebox.FileName+".ruim-*"))
	if novo == "" || novo == pub || len(ruins) != 1 || !strings.Contains(log.String(), "trocada por uma nova") {
		t.Errorf("estragado: novo=%v ruins=%d\n%s", novo != "", len(ruins), log.String())
	}
}
