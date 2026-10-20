//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Aula 20.6 (S7): a pasta de dados não pode ser um link, e como root tudo dentro passa a ser
// de root, sem links.
func TestPastaDeDadosProtegida(t *testing.T) {
	base := t.TempDir()
	alvo := filepath.Join(base, "outro-lugar")
	_ = os.Mkdir(alvo, 0o777)
	link := filepath.Join(base, "dados-link")
	_ = os.Symlink(alvo, link)
	if err := protectDataDir(link, func(string) {}); err == nil || !strings.Contains(err.Error(), "não é uma pasta comum") {
		t.Errorf("pasta que é link: %v", err)
	}

	dir := filepath.Join(base, "dados")
	if err := protectDataDir(dir, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("permissão %v", fi.Mode().Perm())
	}
	if os.Geteuid() != 0 {
		t.Skip("a troca de dono só roda como root")
	}
	// Plantado antes: um arquivo de outro dono e um link para fora.
	plantado := filepath.Join(dir, "pacotes")
	_ = os.Mkdir(plantado, 0o777)
	_ = os.Chown(plantado, 12345, 12345)
	_ = os.Symlink(alvo, filepath.Join(dir, "logs"))
	var notas []string
	if err := protectDataDir(dir, func(n string) { notas = append(notas, n) }); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(plantado)
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 0 || st.Gid != 0 {
		t.Errorf("dono %d:%d", st.Uid, st.Gid)
	}
	if _, err := os.Lstat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Error("o link ficou")
	}
	if j := strings.Join(notas, "|"); !strings.Contains(j, "dono trocado para root: pacotes (era uid 12345)") || !strings.Contains(j, "link simbólico apagado: logs") {
		t.Errorf("notas: %q", notas)
	}
}
