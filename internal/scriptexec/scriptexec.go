// Package scriptexec executa, no agente, o job script (Aula 23.8): baixa o bundle (ZIP) do
// servidor, confere o tamanho e o SHA-256, extrai numa pasta só daquele job, roda o ponto de
// entrada e devolve a saída.
//
// A ordem importa, como no tpexec: nada é extraído antes de o ZIP conferir com o SHA-256 que
// veio no job assinado, e nada é executado antes de a extração terminar. O comando é montado
// aqui, a partir do runner; o ponto de entrada e os argumentos vão como argv, nunca por um
// shell — então um nome de arquivo com "&" ou "|" dentro do ZIP não vira outro comando.
//
// A pasta de trabalho da execução é a pasta extraída: o script começa ao lado dos arquivos
// que ele chama (o .exe, o .ini), e pode se referir a eles por caminho relativo. Terminado o
// job, a pasta inteira é apagada.
package scriptexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/bundle"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// ContentPath é o caminho do download no servidor.
const ContentPath = protocol.BundlesPath

// MaxOutput: quanto da saída do script vai no resultado do job. O resto é cortado, com
// aviso: o detalhe do resultado é uma coluna de texto, não um arquivo de log.
const MaxOutput = 4000

// FileName: o nome do ZIP no servidor e no disco da máquina.
func FileName(p jobs.ScriptParams) string { return p.SHA256 + ".zip" }

// Result é o que a execução produziu, para o agente montar o detalhe.
type Result struct {
	Code      int
	Output    string
	Took      time.Duration
	Truncated bool
}

// RunFunc executa o programa na pasta dir e devolve a saída combinada e o código de saída.
// O executor injeta o de verdade (RunLocal); os testes, um falso.
type RunFunc func(ctx context.Context, dir, name string, args []string) ([]byte, int, error)

// Executor executa os jobs script.
type Executor struct {
	// Download baixa um caminho do servidor (a credencial da máquina vai junto).
	Download func(ctx context.Context, path string, w io.Writer, limit int64) (int64, error)
	Dir      string  // onde os bundles são baixados e extraídos (pasta de dados do agente)
	Run      RunFunc // nil: RunLocal
	// Shell: o caminho absoluto de cada runner nesta máquina. Vazio para um runner = este
	// sistema não o tem, e o job é recusado em vez de executado pela metade.
	Shell map[string]string
}

// DefaultShells devolve os caminhos dos runners deste sistema.
func DefaultShells() map[string]string {
	if runtime.GOOS == "windows" {
		sys := os.Getenv("SystemRoot")
		if sys == "" {
			sys = `C:\Windows`
		}
		return map[string]string{
			bundle.RunnerPowerShell: filepath.Join(sys, `System32\WindowsPowerShell\v1.0\powershell.exe`),
			bundle.RunnerCmd:        filepath.Join(sys, `System32\cmd.exe`),
			bundle.RunnerExe:        "", // o próprio arquivo é o programa
			bundle.RunnerVBScript:   filepath.Join(sys, `System32\cscript.exe`),
		}
	}
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if st, err := os.Stat(sh); err == nil && st.Mode().IsRegular() {
			return map[string]string{bundle.RunnerBash: sh}
		}
	}
	return map[string]string{}
}

// Execute roda o job e devolve o detalhe para o servidor. Erros com jobs.ErrRefused viram
// "rejeitado" (nada foi executado na máquina); os outros, "falhou".
func (e *Executor) Execute(ctx context.Context, jobID int64, p jobs.ScriptParams) (string, error) {
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrRefused, err)
	}
	if bundle.OSForRunner(p.Runner) != thisOS() {
		return "", fmt.Errorf("%w: o bundle é para %s e esta máquina é %s", jobs.ErrRefused, bundle.OSForRunner(p.Runner), thisOS())
	}
	prog, ok := e.shellFor(p.Runner)
	if !ok {
		return "", fmt.Errorf("%w: esta máquina não tem o interpretador de %q", jobs.ErrRefused, p.Runner)
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	zipPath := filepath.Join(e.Dir, FileName(p))
	work := filepath.Join(e.Dir, fmt.Sprintf("job-%d", jobID))
	defer os.Remove(zipPath)
	defer os.RemoveAll(work) // nem o bundle nem o que ele extraiu ficam na máquina

	if err := e.fetch(ctx, zipPath, p); err != nil {
		return "", err
	}
	if err := os.RemoveAll(work); err != nil {
		return "", fmt.Errorf("%w: limpar a pasta do job: %v", jobs.ErrNotStarted, err)
	}
	n, err := bundle.Extract(zipPath, work)
	if err != nil {
		return "", fmt.Errorf("%w: extrair o bundle: %v", jobs.ErrRefused, err)
	}
	entry := filepath.Join(work, filepath.FromSlash(p.Entry))
	if st, err := os.Stat(entry); err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("%w: o ponto de entrada %q não está no bundle", jobs.ErrRefused, p.Entry)
	}
	if p.Runner == bundle.RunnerCmd {
		if err := jobs.ValidateScriptArgs(bundle.RunnerCmd, []string{entry}); err != nil {
			return "", fmt.Errorf("%w: caminho de trabalho incompatível com Batch: %v", jobs.ErrRefused, err)
		}
	}

	name, args := command(prog, p.Runner, entry, p.Args)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
	defer cancel()
	run := e.Run
	if run == nil {
		run = RunLocal
	}
	start := time.Now()
	out, code, runErr := run(runCtx, work, name, args)
	took := time.Since(start).Round(time.Second)

	res := Result{Code: code, Took: took}
	res.Output, res.Truncated = clip(out)
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return detail(p, n, res), fmt.Errorf("o script passou do prazo de %d s e foi interrompido", p.Timeout)
	case runCtx.Err() != nil:
		return detail(p, n, res), fmt.Errorf("execução cancelada: %w", runCtx.Err())
	case errors.Is(runErr, jobs.ErrNotStarted):
		return detail(p, n, res), runErr
	case code != 0:
		return detail(p, n, res), fmt.Errorf("o script saiu com código %d", code)
	case runErr != nil:
		return detail(p, n, res), fmt.Errorf("execução do script: %w", runErr)
	}
	return detail(p, n, res), nil
}

// command monta o argv. O arquivo vai como argumento do interpretador, nunca concatenado
// numa linha de comando.
func command(prog, runner, entry string, args []string) (string, []string) {
	switch runner {
	case bundle.RunnerPowerShell:
		return prog, append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", entry}, args...)
	case bundle.RunnerCmd:
		// cmd.exe tem regras próprias, diferentes das do argv de um executável.
		// Operadores/expansões foram recusados na validação; cada valor é delimitado.
		words := []string{`"` + entry + `"`}
		for _, a := range args {
			words = append(words, `"`+a+`"`)
		}
		return prog, []string{"/d", "/v:off", "/s", "/c", `"` + strings.Join(words, " ") + `"`}
	case bundle.RunnerVBScript:
		return prog, append([]string{"//NoLogo", "//B", entry}, args...)
	case bundle.RunnerBash:
		return prog, append([]string{entry}, args...)
	default: // RunnerExe: o próprio arquivo
		return entry, args
	}
}

func (e *Executor) shellFor(runner string) (string, bool) {
	if runner == bundle.RunnerExe {
		return "", true // o programa é o próprio arquivo extraído
	}
	shells := e.Shell
	if shells == nil {
		shells = DefaultShells()
	}
	p, ok := shells[runner]
	return p, ok && p != ""
}

func (e *Executor) fetch(ctx context.Context, zipPath string, p jobs.ScriptParams) error {
	tmp, err := os.CreateTemp(e.Dir, ".baixando-*")
	if err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := e.Download(ctx, ContentPath+FileName(p), io.MultiWriter(tmp, h), p.Size)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w (download do bundle: %v)", jobs.ErrNotStarted, err)
	}
	if n != p.Size {
		return fmt.Errorf("%w: o servidor entregou %d bytes; o job diz %d", jobs.ErrRefused, n, p.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return fmt.Errorf("%w: o ZIP recebido tem SHA-256 %s…, não o do job (%s…)", jobs.ErrRefused, got[:12], p.SHA256[:12])
	}
	return os.Rename(tmp.Name(), zipPath)
}

// waitDelay: depois de o prazo estourar e o processo ser morto, quanto o agente ainda espera
// pela saída. Sem isso, um script que deixa um processo em segundo plano (um serviço, um
// "start /b") mantém o cano da saída aberto, e o Wait ficaria preso nele muito depois do
// prazo — o job nunca terminaria.
const waitDelay = 5 * time.Second

// RunLocal executa de verdade: saída combinada, na pasta do bundle.
func RunLocal(ctx context.Context, dir, name string, args []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- programa e argumentos conferidos pelo pacote jobs; nada vem de shell
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := runProcess(ctx, cmd)
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	return buf.Bytes(), code, err
}

func clip(out []byte) (string, bool) {
	s := strings.ToValidUTF8(strings.TrimRight(string(out), "\r\n \t"), "�")
	if len(s) <= MaxOutput {
		return s, false
	}
	// Corta o começo: o fim de um log costuma dizer mais (o erro, o resumo).
	s = s[len(s)-MaxOutput:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return s, true
}

func detail(p jobs.ScriptParams, files int, r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "bundle %s · %s · %d arquivo(s) · código %d · %s", p.Bundle, p.Entry, files, r.Code, r.Took)
	if r.Truncated {
		b.WriteString(" · saída cortada no fim")
	}
	if r.Output != "" {
		b.WriteString("\n")
		b.WriteString(r.Output)
	}
	return b.String()
}

func thisOS() string {
	if runtime.GOOS == "windows" {
		return bundle.OSWindows
	}
	return bundle.OSUnix
}
