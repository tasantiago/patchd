package platform

import (
	"errors"
	"strings"
)

// Process é um processo em execução (Aula 14.6b): o nome do executável, o PID e a sessão
// do Windows (0 é a dos serviços; 1 em diante, as de quem fez logon).
type Process struct {
	Name    string
	PID     uint32
	Session uint32
}

// ErrNoProcessList: o sistema não lista processos por aqui (fora do Windows).
var ErrNoProcessList = errors.New("lista de processos só no Windows")

// Running filtra, de uma lista de processos, os que têm um dos nomes, sem diferença de
// caixa (o Windows não diferencia Code.exe de code.exe).
func Running(list []Process, names []string) []Process {
	var out []Process
	for _, p := range list {
		for _, n := range names {
			if strings.EqualFold(p.Name, n) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
