// Comando patchd-agent: agente que coleta inventário, escaneia patches e executa jobs.
package main

import (
	"fmt"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/platform"
)

func main() {
	// Identificação do binário: versão (ldflags ou git) e commit gravado pelo Go.
	fmt.Printf("patchd-agent %s\n", buildinfo.Get())
	// Valores escolhidos na compilação conforme o GOOS (arquivos _windows, _linux, _darwin).
	fmt.Printf("plataforma: %s | unix: %t | dados: %s\n", platform.Name, platform.IsUnix, platform.DataDir())
}
