//go:build linux || darwin

package localapi

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPeerIdentityComesFromKernel(t *testing.T) {
	dir,err:=os.MkdirTemp("","pd-");if err!=nil{t.Fatal(err)};t.Cleanup(func(){_ = os.RemoveAll(dir)})
 path := filepath.Join(dir, "user.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err == nil {
			defer c.Close()
			_, err = c.Write([]byte("caller cannot choose uid"))
		}
		done <- err
	}()
	c, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, err := peerUID(c)
	if err != nil || uint64(uid) != uint64(os.Getuid()) {
		t.Fatalf("peer uid=%d err=%v", uid, err)
	}
	b := make([]byte, 100)
	if _, err := c.Read(b); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := unixIdentity(0); err == nil {
		t.Fatal("aceitou root como usuário da sessão")
	}
	if uid != 0 {
		tok, err := unixIdentity(uid)
		if err != nil {
			t.Fatal(err)
		}
		id, err := IdentityFrom(tok)
		if err != nil || id.Account == "" {
			t.Fatalf("identidade: %+v %v", id, err)
		}
	}
}
