//go:build !windows

package main

import "log/slog"

// syncUserApp: o app do usuário é só do Windows.
func syncUserApp(logger *slog.Logger, enabled bool) {}

// managedAppPath: fora do Windows não há cópia do app para vigiar.
func managedAppPath() string { return "" }
