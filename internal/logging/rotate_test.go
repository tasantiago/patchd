package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "agent.log")
	r, err := OpenRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	linha := strings.Repeat("x", 39) + "\n" // 40 bytes
	for range 7 {                           // 280 bytes: cabem 2 registros por arquivo
		if _, err := r.Write([]byte(linha)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	tamanho := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			return -1
		}
		return st.Size()
	}
	// 7 registros, 2 por arquivo: atual com 1, .1 com 2, .2 com 2; os 2 mais antigos foram descartados.
	if tamanho(path) != 40 || tamanho(path+".1") != 80 || tamanho(path+".2") != 80 || tamanho(path+".3") != -1 {
		t.Errorf("tamanhos: atual %d, .1 %d, .2 %d, .3 %d", tamanho(path), tamanho(path+".1"), tamanho(path+".2"), tamanho(path+".3"))
	}

	// Reabrir continua do tamanho atual, sem perder o que já estava lá.
	r, _ = OpenRotating(path, 100, 2)
	_, _ = r.Write([]byte(linha))
	r.Close()
	if tamanho(path) != 80 {
		t.Errorf("reabrir deve continuar o arquivo: %d", tamanho(path))
	}
}

func TestRegistroMaiorQueOLimiteNaoTrava(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	r, _ := OpenRotating(path, 10, 1)
	defer r.Close()
	for range 3 {
		if _, err := r.Write([]byte(strings.Repeat("y", 50))); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := os.Stat(path)
	if st.Size() != 50 {
		t.Errorf("um registro grande ocupa um arquivo sozinho: %d", st.Size())
	}
}
