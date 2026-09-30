package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executa comandos do sistema. A implementação real (ExecRunner) força
// LC_ALL=C e um prazo máximo; nos testes, um fake devolve saídas gravadas de máquinas reais.
type Runner interface {
	// Run executa name (sempre um caminho absoluto) com args e devolve a saída padrão.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// defaultTimeout é o prazo usado quando ExecRunner.Timeout não é definido.
const defaultTimeout = 30 * time.Second

// maxStderr limita quanto da saída de erro entra na mensagem de erro.
const maxStderr = 500

// ExecRunner executa comandos de verdade.
type ExecRunner struct {
	Timeout time.Duration // prazo por comando; zero usa defaultTimeout
}

// Run executa o comando com LC_ALL=C e prazo máximo. Recusa nomes relativos:
// o agente roda como SYSTEM/root, e resolver pelo PATH permitiria trocar o executável.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !isAbs(name) {
		return nil, fmt.Errorf("comando %q: use o caminho absoluto do executável", name)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	// Saída sempre em inglês e sem formatação regional. Com chaves repetidas,
	// o os/exec usa o último valor, então estas vencem as herdadas do ambiente.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("%s: prazo de %s esgotado", name, timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxStderr {
			msg = msg[:maxStderr] + "…"
		}
		return out, fmt.Errorf("%s: %w (stderr: %s)", name, err, msg)
	}
	return out, nil
}
