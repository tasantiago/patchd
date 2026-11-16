// Package macinstall executa, no agente do macOS, o job thirdparty com o instalador
// "installomator": baixa o Installomator.sh do servidor, confere o SHA-256 e o tamanho do job
// assinado e o roda como root com o rótulo do programa.
//
// O Installomator baixa do fabricante, confere o Team ID da assinatura da Apple e instala. O
// patchd o chama sempre do mesmo jeito: sem aviso na tela (NOTIFY=silent), sem fechar programa
// aberto de ninguém (BLOCKING_PROCESS_ACTION=silent_fail: com o programa aberto, sai e o job
// volta adiado, como no Windows), sem interromper apresentação (INTERRUPT_DND=no) e sem reabrir
// nada. A saída inteira vai para logs/installomator-job-N.log.
package macinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Timeout: o prazo de uma instalação (o download vem do fabricante, pela rede da máquina).
const Timeout = 60 * time.Minute

// maxOutput: o que fica da saída na memória (o log tem tudo).
const maxOutput = 64 << 10

// Executor instala pelo Installomator.
type Executor struct {
	// Download baixa do servidor (agent.Client.Download).
	Download func(ctx context.Context, path string, w io.Writer, limit int64) (int64, error)
	Dir      string // onde o script fica (pasta de dados do agente, só root)
	LogDir   string
	Shell    string // /bin/zsh (o Installomator é zsh)
	// Run executa e devolve a saída (stdout e stderr juntos) e o código de saída; nil: exec.
	Run     func(ctx context.Context, name string, args []string, log io.Writer) (out []byte, code int, err error)
	Timeout time.Duration
}

// args: as variáveis fixas do Installomator, depois do rótulo.
var args = []string{"DEBUG=0", "NOTIFY=silent", "BLOCKING_PROCESS_ACTION=silent_fail", "REOPEN=no", "INTERRUPT_DND=no",
	"LOGO=appstore", "IGNORE_APP_STORE_APPS=no", "SYSTEMOWNER=0"}

// Install executa o job. O erro segue as convenções do agente: jobs.ErrRefused (recusado),
// jobs.ErrNotStarted (nada rodou), jobs.ErrRetry (adiado).
func (e *Executor) Install(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	if p.Installer != jobs.InstallerInstallomator {
		return "", fmt.Errorf("%w: no macOS o patchd instala pelo Installomator, não pelo instalador %q", jobs.ErrRefused, p.Installer)
	}
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrRefused, err)
	}
	label := p.Args[0]
	script, err := e.fetch(ctx, p)
	if err != nil {
		return "", err
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var logw io.Writer = io.Discard
	if e.LogDir != "" {
		if err := os.MkdirAll(e.LogDir, 0o700); err == nil {
			f, err := os.OpenFile(filepath.Join(e.LogDir, fmt.Sprintf("installomator-job-%d.log", jobID)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err == nil {
				defer f.Close()
				logw = f
			}
		}
	}
	run := e.Run
	if run == nil {
		run = execRun
	}
	out, code, err := run(ctx, e.Shell, append([]string{script, label}, args...), logw)
	if ctx.Err() == context.DeadlineExceeded {
		return lastLine(out), fmt.Errorf("o Installomator passou de %s", timeout)
	}
	if err != nil && code < 0 {
		return "", fmt.Errorf("%w: o Installomator não rodou: %v", jobs.ErrNotStarted, err)
	}
	return result(label, code, out)
}

// result traduz o código de saída do Installomator (a lista do próprio script).
func result(label string, code int, out []byte) (string, error) {
	last := lastLine(out)
	switch code {
	case 0:
		return "Installomator " + label + ": " + last, nil
	case 1:
		return last, fmt.Errorf("%w: esta versão do Installomator não conhece o rótulo %q", jobs.ErrRefused, label)
	case 10, 11, 12:
		return last, fmt.Errorf("%w: %w: o programa está aberto (o patchd não fecha programa de quem está trabalhando)", jobs.ErrRetry, jobs.ErrProgramOpen)
	case 24, 25:
		return last, fmt.Errorf("%w: há uma apresentação ou chamada em tela cheia", jobs.ErrRetry)
	case 2, 77:
		return last, fmt.Errorf("%w: o Installomator não conseguiu baixar do fabricante (código %d)", jobs.ErrNotStarted, code)
	case 5:
		return last, fmt.Errorf("o Team ID da assinatura do arquivo baixado não é o esperado: o Installomator recusou instalar")
	case 15:
		return last, fmt.Errorf("%w: o macOS desta máquina é antigo demais para o programa", jobs.ErrRefused)
	case 23:
		return last, fmt.Errorf("%w: o programa veio da App Store, que cuida da atualização dele", jobs.ErrRefused)
	}
	return last, fmt.Errorf("o Installomator saiu com o código %d", code)
}

// fetch baixa o script para a pasta do agente e confere o SHA-256 e o tamanho do job.
func (e *Executor) fetch(ctx context.Context, p jobs.ThirdPartyParams) (string, error) {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: pasta do Installomator: %v", jobs.ErrNotStarted, err)
	}
	final := filepath.Join(e.Dir, p.SHA256+".sh")
	if ok, _ := matches(final, p.SHA256, p.Size); ok {
		return final, nil
	}
	tmp, err := os.CreateTemp(e.Dir, ".baixando-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	defer os.Remove(tmp.Name()) // sem efeito depois do Rename
	h := sha256.New()
	n, err := e.Download(ctx, protocol.PackagesPath+p.SHA256+".sh", io.MultiWriter(tmp, h), p.Size)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		err = fmt.Errorf("%w: download do Installomator: %v", jobs.ErrNotStarted, err)
		if jobs.Transient(err) {
			err = fmt.Errorf("%w: %w", jobs.ErrRetry, err)
		}
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != p.Size || got != p.SHA256 {
		return "", fmt.Errorf("%w: o Installomator baixado (%d bytes, SHA-256 %s…) não é o do job", jobs.ErrRefused, n, got[:12])
	}
	if err := os.Chmod(tmp.Name(), 0o400); err != nil { // o zsh só lê: nem escrita, nem execução
		return "", fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	return final, nil
}

// matches: o arquivo existe com o tamanho e o SHA-256.
func matches(path, sha string, size int64) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- caminho montado do SHA-256 conferido, na pasta do agente
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	return err == nil && n == size && hex.EncodeToString(h.Sum(nil)) == sha, err
}

// lastLine: a última linha útil da saída (sem o carimbo de data do Installomator), para o
// detalhe do job.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		// "2026-11-16 10:00:01 : REQ   : googlechromepkg : All done!" → "All done!"
		if parts := strings.Split(l, " : "); len(parts) >= 3 {
			l = strings.TrimSpace(parts[len(parts)-1])
		}
		if r := []rune(l); len(r) > 300 {
			l = string(r[:300]) + "…"
		}
		return l
	}
	return ""
}

// capped guarda só o fim da saída.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	c.Buffer.Write(p)
	if c.Len() > maxOutput {
		rest := c.Bytes()[c.Len()-maxOutput:]
		c.Reset()
		c.Buffer.Write(rest)
	}
	return n, nil
}

// execRun roda o comando sem entrada, com a saída para o log e o fim dela na memória. code -1:
// não chegou a rodar.
func execRun(ctx context.Context, name string, argv []string, log io.Writer) ([]byte, int, error) {
	if !filepath.IsAbs(name) {
		return nil, -1, fmt.Errorf("comando %q: use o caminho absoluto", name)
	}
	var out capped
	// #nosec G204 -- o shell é fixo (/bin/zsh); o script tem o SHA-256 do job assinado e o rótulo foi conferido
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Stdout = io.MultiWriter(&out, log)
	cmd.Stderr = cmd.Stdout
	cmd.Dir = "/"
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return out.Bytes(), exit.ExitCode(), nil
	case err != nil:
		return out.Bytes(), -1, err
	}
	return out.Bytes(), 0, nil
}
