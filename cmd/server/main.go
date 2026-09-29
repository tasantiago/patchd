// Comando patchd-server: API, catálogo, motor de compliance e fila de jobs.
package main

import (
	"fmt"
	"runtime"
)

func main() {
	// Por enquanto só se identifica. HTTP e banco entram no Módulo 4.
	fmt.Printf("patchd-server (%s/%s): esqueleto da Aula 1.1\n", runtime.GOOS, runtime.GOARCH)
}
