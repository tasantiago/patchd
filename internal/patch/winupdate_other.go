//go:build !windows

package patch

// NewWinUpdater: fora do Windows não há job windows_update. nil faz o agente recusar o
// job, dizendo por quê.
func NewWinUpdater() WinUpdater { return nil }
