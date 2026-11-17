//go:build !windows

package userapp

import (
	"context"
	"time"
)

// OpenRequests: o pedido de abrir a página entre execuções é do Windows (o atalho da área
// de trabalho); fora dele, nenhum pedido chega.
func OpenRequests(context.Context) (<-chan struct{}, error) { return nil, nil }

// RequestOpen: só no Windows.
func RequestOpen(time.Duration) bool { return false }
