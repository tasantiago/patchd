//go:build linux || darwin

package userapp

import (
	"testing"
)

func TestSessionAppSingleInstance(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	release, ok := SingleInstance()
	if !ok {
		t.Fatal("primeira instância recusada")
	}
	defer release()
	release2, ok := SingleInstance()
	if ok {
		release2()
		t.Fatal("aceitou duas instâncias na mesma sessão")
	}
}
