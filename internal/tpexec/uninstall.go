package tpexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
)

// A regularização (jobs.ReplaceEntry): antes de instalar o aprovado, o agente remove em
// silêncio a instalação de outro tipo. O comando vem do registro da própria máquina, lido na
// hora (o job só diz qual entrada e por qual método); o executável tem de ser um caminho
// absoluto que existe. Os desinstaladores do Inno e do NSIS voltam antes de terminar (copiam-se
// para o TEMP e seguem de lá), então o agente espera a entrada sair do registro.

// UninstallEntry: os valores da entrada Uninstall que a remoção usa.
type UninstallEntry struct {
	DisplayName, DisplayVersion   string
	QuietUninstall, UninstallLine string
}

// Uninstaller remove a instalação antiga.
type Uninstaller struct {
	Run     platform.Runner
	Msiexec string // o msiexec.exe do sistema, por caminho absoluto
	// Lookup lê a entrada (a chave do job); found false: ela não existe.
	Lookup func(key string) (UninstallEntry, bool, error)
	// Split separa a linha de comando do registro (windows.DecomposeCommandLine).
	Split   func(line string) ([]string, error)
	Timeout time.Duration // o prazo do desinstalador (0: 30 min)
	Wait    time.Duration // quanto esperar a entrada sair depois que ele volta (0: 15 min)
	Poll    time.Duration // de quanto em quanto conferir (0: 5 s)
	Sleep   func(ctx context.Context, d time.Duration) error
}

// Códigos de saída que dizem "removido" (ou "já não estava": 1605 do msiexec).
var uninstallOK = map[int]string{0: "ok", 3010: "reinício pendente", 1641: "reinício iniciado", 1605: "já não estava instalado"}

// command monta o programa e os argumentos pela entrada e pelo método.
func (u *Uninstaller) command(r jobs.ReplaceEntry, e UninstallEntry, log string) (string, []string, error) {
	switch r.Method {
	case "msi":
		args := []string{"/x", r.ProductCode, "/qn", "/norestart"}
		if log != "" && !strings.ContainsAny(log, ` "`) {
			args = append(args, "/l*v", log)
		}
		return u.Msiexec, args, nil
	case "quiet", "inno":
		line := e.QuietUninstall
		if r.Method == "inno" {
			line = e.UninstallLine
		}
		if strings.TrimSpace(line) == "" {
			return "", nil, errors.New("a entrada não tem o comando de desinstalação")
		}
		parts, err := u.Split(line)
		if err != nil || len(parts) == 0 {
			return "", nil, fmt.Errorf("comando de desinstalação ilegível: %q", line)
		}
		prog := parts[0]
		if !filepath.IsAbs(prog) || !strings.EqualFold(filepath.Ext(prog), ".exe") {
			return "", nil, fmt.Errorf("o desinstalador registrado não é um .exe por caminho absoluto: %q", prog)
		}
		if info, err := os.Stat(prog); err != nil || !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("o desinstalador registrado não está no disco: %s", prog)
		}
		args := parts[1:]
		if r.Method == "inno" {
			args = append(args, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART")
		}
		return prog, args, nil
	}
	return "", nil, fmt.Errorf("método %q", r.Method)
}

// Remove remove a entrada e devolve a nota para o detalhe do job.
func (u *Uninstaller) Remove(ctx context.Context, r jobs.ReplaceEntry, log string) (string, error) {
	e, found, err := u.Lookup(r.Key)
	switch {
	case err != nil:
		return "", fmt.Errorf("ler a entrada %s: %w", r.Key, err)
	case !found:
		return "a instalação anterior já não estava registrada", nil
	case !strings.EqualFold(strings.TrimSpace(e.DisplayName), r.Name):
		return "", fmt.Errorf("%w: a entrada %s agora é de %q, não de %q", jobs.ErrRefused, r.Key, e.DisplayName, r.Name)
	}
	prog, args, err := u.command(r, e, log)
	if err != nil {
		return "", err
	}
	timeout := cmpDur(u.Timeout, 30*time.Minute)
	rctx, cancel := context.WithTimeout(ctx, timeout)
	_, runErr := u.Run.Run(rctx, prog, args...)
	cancel()
	code := 0
	var exitErr interface{ ExitCode() int }
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
	default:
		return "", fmt.Errorf("o desinstalador não terminou: %w", runErr)
	}
	what, ok := uninstallOK[code]
	if !ok {
		return "", fmt.Errorf("o desinstalador terminou com o código %d", code)
	}
	// Espera a entrada sair do registro (o Inno e o NSIS seguem depois de voltar).
	sleep := u.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	deadline := time.Now().Add(cmpDur(u.Wait, 15*time.Minute))
	for {
		_, still, err := u.Lookup(r.Key)
		if err == nil && !still {
			break
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("o desinstalador voltou (código %d), mas a entrada %s continua registrada", code, r.Name)
		}
		if err := sleep(ctx, cmpDur(u.Poll, 5*time.Second)); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("instalação anterior removida (%s %s, %s, código %s: %s)", r.Name, r.Version, r.Method, strconv.Itoa(code), what), nil
}

func cmpDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
