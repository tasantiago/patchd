package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/platform"
)

const (
	// installLockFile fica na pasta de dados e serializa install e enroll.
	installLockFile = "install.lock"
	installLockPoll = 500 * time.Millisecond
)

// installLockWait: quanto um install (ou enroll) espera outro que esteja em andamento,
// por exemplo o script de inicialização da GPO e um job do ITOM no mesmo boot.
var installLockWait = 5 * time.Minute

// serviceState é a situação do serviço instalado em relação ao que este install instalaria.
type serviceState int

const (
	serviceMissing  serviceState = iota // não instalado
	serviceCurrent                      // mesmo executável e mesmas opções
	serviceOutdated                     // instalado, mas com outro executável ou outras opções
)

// runInstall é a instalação para a frota: um comando só, que registra a máquina (se ainda
// não estiver registrada) e instala ou atualiza o serviço. É repetível: rodar de novo com
// o mesmo executável e as mesmas opções não muda nada. É isso que permite colocá-lo num
// script de inicialização da GPO, que roda a cada boot, ou num job do ITOM que pode ser
// disparado de novo.
func runInstall(args []string, look config.Lookup, stdout, stderr io.Writer, agentVersion string) int {
	cfg, err := loadAgentConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	if why, refuse := containerCheck(allowContainer(look)); refuse {
		fmt.Fprintf(stderr, "patchd-agent: não instalo dentro de container (%s)\n", why)
		return exitConfig
	}
	// A reinstalação apaga o executável instalado; rodando dele mesmo, o install apagaria
	// o arquivo de onde precisa copiar.
	if runningInstalledCopy() {
		fmt.Fprintf(stderr, "patchd-agent: rode o install a partir do executável distribuído (pacote, compartilhamento), não do instalado em %s\n", installedPath())
		return exitConfig
	}

	// A pasta é protegida antes de qualquer coisa ser gravada nela (trava e credencial).
	if err := protectDataDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: proteger %s: %v\n", cfg.DataDir, err)
		return exitRuntime
	}
	unlock, err := lockDataDir(cfg.DataDir, installLockWait, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	defer unlock()

	// O serviço é conferido antes do registro: um sistema em que o serviço não pode ser
	// instalado (sem systemd, por exemplo) falha aqui, sem gastar um uso do token.
	svcArgs := serviceArgs(args)
	state, err := installedState(cfg, svcArgs)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: conferir o serviço instalado: %v\n", err)
		return exitRuntime
	}

	fmt.Fprintf(stdout, "patchd-agent %s\n", agentVersion)
	if _, code := ensureEnrolled(cfg, look, stdout, stderr, agentVersion); code != exitOK {
		return code
	}

	switch state {
	case serviceMissing:
		return serviceInstall(svcArgs, look, stdout, stderr)
	case serviceCurrent:
		if err := serviceStart(); err != nil {
			fmt.Fprintf(stderr, "patchd-agent: iniciar o serviço: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintln(stdout, "serviço: já instalado com este executável e estas opções; em execução")
		return exitOK
	default:
		fmt.Fprintln(stdout, "serviço: executável ou opções diferentes; reinstalando (a credencial fica)")
		if code := serviceUninstall(io.Discard, stderr); code != exitOK {
			return code
		}
		return serviceInstall(svcArgs, look, stdout, stderr)
	}
}

// ensureEnrolled garante a credencial. Se a máquina já está registrada no mesmo servidor,
// não registra de novo e nem precisa do token: o boot seguinte, que roda o mesmo script,
// não pode virar outro registro (e outro alerta de "reenrollment" no servidor).
func ensureEnrolled(cfg agentConfig, look config.Lookup, stdout, stderr io.Writer, agentVersion string) (credential.Credential, int) {
	c, err := credential.Load(cfg.DataDir)
	switch {
	case err == nil:
		if cfg.ServerURL != "" && !sameServer(cfg.ServerURL, c.ServerURL) {
			fmt.Fprintf(stderr, "patchd-agent: esta máquina já está registrada em outro servidor (%s); "+
				"para trocar, remova %s e rode de novo\n", c.ServerURL, credential.Path(cfg.DataDir))
			return c, exitConfig
		}
		fmt.Fprintf(stdout, "registro: já registrada como %s\n", c.MachineID)
		return c, exitOK
	case !errors.Is(err, credential.ErrNotEnrolled):
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return c, exitRuntime
	}

	if cfg.ServerURL == "" {
		fmt.Fprintln(stderr, "patchd-agent: máquina não registrada: informe o servidor (-server ou PATCHD_SERVER_URL)")
		return c, exitConfig
	}
	token, err := enrollToken(cfg.EnrollTokenFile, look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: máquina não registrada: %v\n", err)
		return c, exitConfig
	}
	c, err = enrollMachine(cfg.ServerURL, cfg.DataDir, token, agentVersion, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return c, exitRuntime
	}
	fmt.Fprintf(stdout, "registro: máquina registrada como %s\n", c.MachineID)
	return c, exitOK
}

// lockDataDir obtém a trava de instalação na pasta de dados, esperando até wait se outro
// install ou enroll estiver em andamento.
func lockDataDir(dataDir string, wait time.Duration, stderr io.Writer) (func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("pasta de dados: %w", err)
	}
	path := filepath.Join(dataDir, installLockFile)
	deadline := time.Now().Add(wait)
	warned := false
	for {
		unlock, err := platform.TryLock(path)
		if !errors.Is(err, platform.ErrLocked) {
			return unlock, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("outra instalação ou registro continua em andamento depois de %s (%s)", wait, path)
		}
		if !warned {
			fmt.Fprintln(stderr, "patchd-agent: outra instalação ou registro em andamento; aguardando")
			warned = true
		}
		time.Sleep(installLockPoll)
	}
}

// serviceArgs tira das opções do install as que só servem ao registro: o servidor vem da
// credencial, e o caminho do token não tem o que fazer na linha de comando do serviço.
// O que sobra vira a linha de comando do serviço, exatamente como no "service install".
func serviceArgs(args []string) []string {
	out := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && (name == "server" || name == "enroll-token-file") {
			if !hasValue {
				i++ // o valor vem no argumento seguinte
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// runningInstalledCopy diz se este processo é o próprio executável instalado do serviço.
func runningInstalledCopy() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	a, err := os.Stat(self)
	if err != nil {
		return false
	}
	b, err := os.Stat(installedPath())
	return err == nil && os.SameFile(a, b)
}

// sameServer compara URLs de servidor ignorando a barra final.
func sameServer(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// sameExecutable diz se o arquivo em path é idêntico, byte a byte, ao executável que está
// rodando. Arquivo ausente: false.
func sameExecutable(path string) (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return false, err
	}
	installed, err := fileSHA256(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	running, err := fileSHA256(self)
	if err != nil {
		return false, err
	}
	return installed == running, nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
