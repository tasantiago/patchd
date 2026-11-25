//go:build !windows

package hoststate

import (
	"context"
	"fmt"

	"github.com/tasantiago/patchd/internal/protocol"
)

func windowsRunningServices(context.Context) (*protocol.RunningServices, error) {
	return nil, fmt.Errorf("SCM indisponível")
}
