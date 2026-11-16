package main

import (
	"io"

	"github.com/tasantiago/patchd/internal/appmain"
	"github.com/tasantiago/patchd/internal/userapp"
)

// runApp: "patchd-agent app", a Central rodando do próprio agente (macOS, Linux e testes). No
// Windows, quem abre no logon é o patchd-app.exe (cmd/app), que o agente traz embutido.
func runApp(args []string, stdout, stderr io.Writer) int {
	return appmain.Run(args, stdout, stderr, userapp.Caller(localCall), managedAppPath())
}
