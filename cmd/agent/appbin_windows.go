//go:build windows && patchdapp

package main

import _ "embed"

// A Central do Windows desta arquitetura, que o scripts/build.sh compila (cmd/app), comprime e
// deixa aqui antes de compilar o agente (fora do git; apagada no fim do build).
var (
	//go:embed patchd-app.exe.gz
	embeddedAppGz []byte
	//go:embed patchd-app.exe.sha256
	embeddedAppSum string
)
