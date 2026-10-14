//go:build !linux && !windows

package hoststate

import (
	"context"

	"github.com/tasantiago/patchd/internal/protocol"
)

// loggedUsers: no macOS, ainda não (o utmpx tem outro formato). nil: não coletado.
func loggedUsers(ctx context.Context) ([]protocol.LoggedUser, error) { return nil, nil }
