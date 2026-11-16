//go:build windows && !patchdapp

package main

// Sem a Central embutida: um build do Windows fora do scripts/build.sh. (Fora do Windows, a
// Central é o próprio agente, "patchd-agent app".)
var (
	embeddedAppGz  []byte
	embeddedAppSum string
)
