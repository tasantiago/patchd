// Comando patchd-app: a Central de software do usuário no Windows (internal/appmain).
//
// Compilado pelo scripts/build.sh como programa gráfico (-H windowsgui) e com o próprio
// recurso de versão; o patchd-agent.exe o leva embutido e o grava, sem mudar nada, ao lado de
// si, como patchd-app.exe. A chave Run do HKLM o abre no logon com "patchd-app.exe app" (o
// argumento é o mesmo de quando a Central era o próprio agente, e continua aceito). Sem
// argumento (o atalho da área de trabalho), a Central abre a página.
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
	os.Exit(appmain.Run(appArgs(os.Args[1:]), os.Stdout, os.Stderr, userapp.Caller(localapi.Call), managed()))
}

// appArgs: os argumentos da Central. "app" é o do logon (a chave Run): o ícone, sem abrir a
// página. Sem argumento nenhum é o usuário que abriu (o atalho da área de trabalho, o
// Explorer): abre a página.
func appArgs(args []string) []string {
	switch {
	case len(args) == 0:
		return []string{"-abrir"}
	case args[0] == "app":
		return args[1:]
	}
	return args
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
