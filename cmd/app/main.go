// Comando patchd-app: a Central de software do usuário no Windows (internal/appmain).
//
// Compilado pelo scripts/build.sh como programa gráfico (-H windowsgui) e com o próprio
// recurso de versão; o patchd-agent.exe o leva embutido e o grava, sem mudar nada, ao lado de
// si, como patchd-app.exe. A chave Run do HKLM o abre no logon com "patchd-app.exe app" (o
// argumento é o mesmo de quando a Central era o próprio agente, e continua aceito).
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tasantiago/patchd/internal/appmain"
	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/userapp"
)

// AppFile é o nome da cópia que o serviço do agente mantém.
const AppFile = "patchd-app.exe"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "app" {
		args = args[1:]
	}
	os.Exit(appmain.Run(args, os.Stdout, os.Stderr, userapp.Caller(localapi.Call), managed()))
}

// managed: o próprio executável, quando é a cópia do serviço (a atualização do agente a troca
// e a Central reabre com a nova); vazio para um executável de teste, com outro nome.
func managed() string {
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Base(exe), AppFile) {
		return ""
	}
	return exe
}
