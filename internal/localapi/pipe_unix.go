//go:build linux || darwin

package localapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/tasantiago/patchd/internal/protocol"
)

var ErrUnsupported = errors.New("canal local indisponível neste sistema")

const socketPath = "/var/run/patchd/user.sock"

type unixConn struct{ *net.UnixConn }

func (c *unixConn) Identify() (TokenInfo, error) {
	uid, err := peerUID(c.UnixConn)
	if err != nil {
		return TokenInfo{}, err
	}
	return unixIdentity(uid)
}

func unixIdentity(uid uint32) (TokenInfo, error) {
	if uid == 0 {
		return TokenInfo{}, errors.New("use a sessão do usuário, sem sudo")
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return TokenInfo{}, err
	}
	if u.HomeDir == "" || u.HomeDir == "/" || u.HomeDir == "/var/empty" || u.HomeDir == "/nonexistent" {
		return TokenInfo{}, errors.New("conta sem perfil de usuário")
	}
	info := TokenInfo{UserSID: "unix:" + u.Uid, Account: u.Username, Groups: []TokenGroup{{SID: sidInteractive, Attrs: groupEnabled}}}
	ids, err := u.GroupIds()
	if err != nil {
		return TokenInfo{}, err
	}
	if len(ids) > protocol.MaxSelfServiceGroups {
		return TokenInfo{}, errors.New("grupos demais")
	}
	for _, id := range ids {
		g, err := user.LookupGroupId(id)
		if err != nil {
			continue
		}
		info.Groups = append(info.Groups, TokenGroup{SID: "unix-group:" + id, Name: g.Name, Attrs: groupEnabled})
	}
	return info, nil
}

func rootOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && info.Mode().Perm()&0022 == 0
}

// Listen usa um diretório de root e autentica o UID do par pelo kernel, nunca pelo JSON.
func Listen(ctx context.Context, handle func(Conn)) error {
	if os.Geteuid() != 0 {
		return errors.New("o serviço precisa de root para abrir o canal local")
	}
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !rootOwned(info) {
		return errors.New("diretório do canal local precisa pertencer a root e não permitir escrita a outros usuários")
	}
	if old, err := os.Lstat(socketPath); err == nil {
		st, ok := old.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || old.Mode()&os.ModeSocket == 0 {
			return errors.New("caminho do canal local já ocupado")
		}
		c, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = c.Close()
			return errors.New("outro agente já atende o canal local")
		}
		if err := os.Remove(socketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer ln.Close()
	// #nosec G302 -- socket acessível aos usuários; o kernel fornece a identidade do par e o diretório só permite escrita a root.
	if err := os.Chmod(socketPath, 0666); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-done:
		}
	}()
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
			wg.Add(1)
			go func() { defer wg.Done(); defer func() { <-sem }(); handle(&unixConn{c}) }()
		default:
			_ = c.Close()
		}
	}
}

func dial(ctx context.Context) (*net.UnixConn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, ErrNoAgent
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, errors.New("canal local inválido")
	}
	uid, err := peerUID(uc)
	if err != nil || uid != 0 {
		_ = c.Close()
		return nil, fmt.Errorf("canal local não pertence ao serviço root: %v", err)
	}
	return uc, nil
}
