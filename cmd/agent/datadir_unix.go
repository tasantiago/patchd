//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// protectDataDir: só root entra na pasta de dados (Aula 4.3; Aula 20.6, S7). Antes, a pasta
// só ganhava 0700: se alguém a tivesse criado antes (um PATCHD_DATA_DIR fora de /var/lib),
// continuaria dono, e o dono muda as permissões quando quiser. Agora:
//
//   - a pasta não pode ser um link simbólico (recusa: o install não segue para outro lugar);
//   - como root (o serviço e o install), tudo dentro passa a ser de root:root, e links
//     simbólicos lá dentro são apagados. note recebe cada correção, para o log.
func protectDataDir(dir string, note func(string)) error {
	if fi, err := os.Lstat(dir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s não é uma pasta comum (link ou arquivo): confira quem a criou, remova e rode de novo", dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// #nosec G302 -- é uma pasta: 0700 é só o dono (root)
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return nil // rodando como usuário (desenvolvimento, testes): nada a trocar de dono
	}
	// Aula 20.6: a varredura passa pelo os.Root (a pasta aberta uma vez): um link trocado no
	// meio do caminho não leva a operação para fora dela (o gosec G122 apontou o WalkDir com
	// caminhos soltos na primeira versão desta aula).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != "." && d.Type()&fs.ModeSymlink != 0 {
			if err := root.Remove(p); err != nil {
				return fmt.Errorf("apagar o link %s: %w", p, err)
			}
			note("link simbólico apagado: " + p)
			return nil
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && (st.Uid != 0 || st.Gid != 0) {
			if err := root.Lchown(p, 0, 0); err != nil {
				return fmt.Errorf("passar %s para root: %w", p, err)
			}
			note(fmt.Sprintf("dono trocado para root: %s (era uid %d)", p, st.Uid))
		}
		return nil
	})
}
