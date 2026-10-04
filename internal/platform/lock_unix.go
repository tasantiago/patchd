//go:build linux || darwin

package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock usa o flock: a trava pertence ao arquivo aberto, então duas aberturas do mesmo
// caminho disputam a trava mesmo dentro de um só processo.
func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		f.Close()
	}, nil
}
