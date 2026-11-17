//go:build !windows && !linux && !darwin

package main

import "log/slog"

// syncUserApp: o app do usuário é só do Windows.
func syncUserApp(logger *slog.Logger, enabled bool, name string) {}

// userAppRenamer: fora do Windows não há cópia da Central para renomear.
func userAppRenamer(*slog.Logger) func(string) (bool, error) { return nil }

// managedAppPath: fora do Windows não há cópia do app para vigiar.
// removeCentralLink: o atalho da Central na área de trabalho é só do Windows.
func removeCentralLink() error { return nil }

func managedAppPath() string { return "" }
