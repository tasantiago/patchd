//go:build !windows

package localapi

import (
	"context"
	"errors"
	"os"
)

// ErrUnsupported: o canal local só existe no Windows (a loja é de programas do Windows).
var ErrUnsupported = errors.New("o autoatendimento local só existe no Windows")

// Listen não faz nada fora do Windows.
func Listen(ctx context.Context, handle func(Conn)) error { return ErrUnsupported }

func dial(ctx context.Context) (*os.File, error) { return nil, ErrUnsupported }
