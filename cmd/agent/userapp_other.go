//go:build !windows

package main

import "log/slog"

// syncUserApp: o app do usuário é só do Windows.
func syncUserApp(logger *slog.Logger, enabled bool, name string) {}

// userAppRenamer: fora do Windows não há cópia da Central para renomear.
func userAppRenamer() func(string) (bool, error) { return nil }

// managedAppPath: fora do Windows não há cópia do app para vigiar.
func managedAppPath() string { return "" }
