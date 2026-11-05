//go:build !windows

package patch

import "github.com/tasantiago/patchd/internal/protocol"

// MicrosoftInfo: fora do Windows, não há fonte Microsoft.
func MicrosoftInfo() *protocol.MicrosoftUpdate { return nil }
