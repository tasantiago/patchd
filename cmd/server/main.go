// Comando patchd-server: API, catálogo, motor de compliance e fila de jobs.
package main

import (
	"fmt"

	"github.com/tasantiago/patchd/internal/buildinfo"
)

func main() {
	// Por enquanto só se identifica. HTTP e banco entram no Módulo 4.
	fmt.Printf("patchd-server %s\n", buildinfo.Get())
}
