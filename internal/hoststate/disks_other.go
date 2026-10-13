//go:build !linux && !darwin && !windows

package hoststate

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/protocol"
)

func disks(ctx context.Context) ([]protocol.Disk, []error) {
	return nil, []error{errors.New("ainda não implementado neste SO")}
}
