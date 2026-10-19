package platform

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// SilentRunner executa um instalador sem ler a saída dele (Aula 14.4). O ExecRunner lê a
// saída padrão por um pipe, e o Wait espera o pipe fechar: um instalador que deixa um
// processo filho rodando (um atualizador, um serviço) com o pipe herdado prenderia o agente
// até o prazo. Sem pipe, o agente espera só o instalador. Os instaladores silenciosos não
// escrevem nada útil na saída; o msiexec escreve no log pedido com /l*v.
type SilentRunner struct {
	Timeout time.Duration // prazo por comando; zero usa defaultTimeout
}

// Run executa name e devolve o erro com o código de saída (*exec.ExitError) quando houver.
func (r SilentRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !isAbs(name) {
		return nil, fmt.Errorf("comando %q: use o caminho absoluto do executável", name)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- executar programas é o trabalho do agente; quem chama passa nomes fixos
	cmd := exec.CommandContext(ctx, name, args...)
	// Stdout e Stderr nil: o processo recebe o dispositivo nulo, não um pipe.
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s: prazo de %s esgotado", name, timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return nil, nil
}
