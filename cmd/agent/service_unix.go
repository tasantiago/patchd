//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
)

const (
	// installedBinary: fora do alcance de usuários comuns (root:root, 0755). Um serviço que
	// roda como root a partir de um arquivo que outro usuário pode trocar é escalada de
	// privilégio pronta.
	installedBinary = "/usr/local/sbin/patchd-agent"
	// serviceManagerEnv é definida pela unidade do systemd e pelo plist do launchd: é como
	// o agente sabe que está rodando como serviço, e não num terminal.
	serviceManagerEnv = "PATCHD_SERVICE_MANAGER"
)

// installedPath é o installedBinary, com a mesma forma de função do Windows.
func installedPath() string { return installedBinary }

func isServiceProcess() bool { return os.Getenv(serviceManagerEnv) != "" }

// runAsService: no systemd e no launchd, a parada chega como SIGTERM; depois do prazo do
// gerenciador (30 s), vem o SIGKILL.
func runAsService(logger *slog.Logger, loop func(context.Context) int) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	return loop(ctx)
}

// protectDataDir: só o dono (root, no serviço) entra na pasta de dados.
func protectDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// requireAdmin recusa instalar ou remover o serviço sem root.
func requireAdmin(stderr io.Writer) bool {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "patchd-agent: instalar ou remover o serviço exige root (use sudo)")
		return false
	}
	return true
}

// writeRootFile grava data em path de forma atômica, com dono root e as permissões dadas.
func writeRootFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // sem efeito depois da renomeação
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if err := os.Chown(name, 0, 0); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// copyBinary copia o executável atual para dest, com dono root e 0755.
func copyBinary(dest string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return err
	}
	if filepath.Clean(src) == filepath.Clean(dest) {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeRootFile(dest, data, 0o755)
}

// runTool executa uma ferramenta do sistema (systemctl, launchctl) e devolve o erro com a
// saída dela, para a mensagem dizer o que o sistema respondeu.
func runTool(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := platform.ExecRunner{Timeout: time.Minute}.Run(ctx, name, args...)
	return err
}

// removeIfExists apaga o arquivo; ausência não é erro.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
