//go:build !windows

package userapp

import (
	"context"
	"errors"
)

// ErrNoTray: o ícone só existe no Windows; fora dele, o app serve a página e mostra o endereço.
var ErrNoTray = errors.New("o ícone da área de notificação só existe no Windows")

// SingleInstance não trava nada fora do Windows.
func SingleInstance() (release func(), ok bool) { return func() {}, true }

// RunTray não existe fora do Windows.
func RunTray(ctx context.Context, opts TrayOptions) error { return ErrNoTray }

// OpenURL não faz nada fora do Windows (o endereço é mostrado no terminal).
func OpenURL(u string) {}

// TrayOptions é o que o ícone precisa.
type TrayOptions struct {
	Tooltip string
	Open    func()
	Notices <-chan Notice
}
