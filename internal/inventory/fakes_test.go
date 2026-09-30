package inventory

import (
	"context"
	"fmt"
	"strings"
)

// runnerFalso responde a comandos pela linha de comando exata ("nome arg1 arg2").
// Comando não previsto é erro: o teste também confere quais comandos o coletor chama.
type runnerFalso map[string]respostaFalsa

type respostaFalsa struct {
	saida string
	err   error
}

func (r runnerFalso) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	linha := strings.Join(append([]string{name}, args...), " ")
	resp, ok := r[linha]
	if !ok {
		return nil, fmt.Errorf("comando não previsto no teste: %s", linha)
	}
	return []byte(resp.saida), resp.err
}

func hostnameFalso() (string, error) { return "maquina-teste", nil }
