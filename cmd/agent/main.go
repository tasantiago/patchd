// Comando patchd-agent: agente que coleta inventário, escaneia patches e executa jobs.
package main

import (
	"fmt"
	"runtime"
)

func main() {
	// Por enquanto só se identifica. A versão via ldflags entra na Aula 1.2.
	fmt.Printf("patchd-agent (%s/%s): esqueleto da Aula 1.1\n", runtime.GOOS, runtime.GOARCH)
}
