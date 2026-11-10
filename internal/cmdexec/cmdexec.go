// Package cmdexec roda, no agente, o comando do terminal do administrador (job comando,
// docs/terminal.md): um interpretador do sistema (PowerShell, cmd ou bash), sem entrada pelo
// teclado, com tempo máximo, a árvore de processos encerrada no fim e só o fim da saída.
package cmdexec

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/scriptexec"
)

// Result: o que o comando devolveu.
type Result struct {
	Output    string
	Truncated bool // a saída passou do limite: ficou o fim
	ExitCode  int
	TimedOut  bool // passou do tempo máximo e foi encerrado
	Took      time.Duration
}

// RunFunc executa o programa (nos testes, um falso): a saída combinada, o código e o erro.
type RunFunc func(ctx context.Context, dir, name string, args []string, max int) ([]byte, int, error)

// Runner roda os comandos.
type Runner struct {
	// Shells: o caminho absoluto de cada interpretador nesta máquina (scriptexec.DefaultShells).
	Shells map[string]string
	// Dir: a pasta em que cada comando começa (a raiz do disco do sistema).
	Dir string
	Run RunFunc // nil: scriptexec.RunLocalMax
}

// New monta o Runner deste sistema.
func New() *Runner {
	dir := "/"
	if runtime.GOOS == "windows" {
		dir = os.Getenv("SystemDrive")
		if dir == "" {
			dir = "C:"
		}
		dir += `\`
	}
	return &Runner{Shells: scriptexec.DefaultShells(), Dir: dir}
}

// Execute roda o comando. Erro (jobs.ErrRefused): o interpretador não existe nesta máquina,
// e nada rodou.
func (r *Runner) Execute(ctx context.Context, p jobs.CommandParams) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", jobs.ErrRefused, err)
	}
	prog := r.Shells[p.Shell]
	if prog == "" {
		return Result{}, fmt.Errorf("%w: esta máquina não tem o interpretador %s", jobs.ErrRefused, p.Shell)
	}
	run := r.Run
	if run == nil {
		run = scriptexec.RunLocalMax
	}
	cctx, cancel := context.WithTimeout(ctx, p.Timeout())
	defer cancel()
	start := time.Now()
	out, code, err := run(cctx, r.Dir, prog, Args(p), jobs.MaxCommandOutput)
	res := Result{ExitCode: code, Took: time.Since(start).Round(100 * time.Millisecond)}
	res.Output, res.Truncated = clip(out)
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		res.TimedOut, res.ExitCode = true, -1
	case errors.Is(err, jobs.ErrNotStarted):
		return res, fmt.Errorf("%w: %v", jobs.ErrRefused, err)
	case err != nil && code == 0:
		res.ExitCode = -1 // não rodou até o fim (o agente está encerrando, por exemplo)
	}
	return res, nil
}

// Args: os argumentos do interpretador para o comando.
//   - PowerShell: -EncodedCommand (o texto em UTF-16, sem regra de aspas no caminho), com a
//     saída em UTF-8 e sem a barra de progresso;
//   - cmd: /d /s /c "..." (o runProcess do Windows entrega a linha como está), com a página de
//     código 65001 para os acentos;
//   - bash: -c.
func Args(p jobs.CommandParams) []string {
	switch p.Shell {
	case jobs.ShellPowerShell:
		script := "[Console]::OutputEncoding=[Text.Encoding]::UTF8;$ProgressPreference='SilentlyContinue';" + p.Command
		u := utf16.Encode([]rune(script))
		b := make([]byte, 0, len(u)*2)
		for _, c := range u {
			b = append(b, byte(c), byte(c>>8))
		}
		return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(b)}
	case jobs.ShellCmd:
		return []string{"/d", "/s", "/c", `"chcp 65001>nul & ` + p.Command + `"`}
	}
	return []string{"-c", p.Command}
}

// clip devolve o fim da saída em UTF-8 válido e se houve corte.
func clip(out []byte) (string, bool) {
	s := strings.ToValidUTF8(strings.TrimRight(string(out), "\r\n \t"), "�")
	if len(s) <= jobs.MaxCommandOutput {
		return s, false
	}
	s = s[len(s)-jobs.MaxCommandOutput:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 400 {
		s = s[i+1:]
	}
	return s, true
}
