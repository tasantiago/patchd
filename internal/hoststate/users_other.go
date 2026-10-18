//go:build !linux && !windows && !darwin

package hoststate

import (
	"context"

	"github.com/tasantiago/patchd/internal/protocol"
)

// loggedUsers: nos outros sistemas, não coletado (nil).
func loggedUsers(ctx context.Context) ([]protocol.LoggedUser, error) { return nil, nil }
