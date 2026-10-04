package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
)

const unitPath = "/etc/systemd/system/" + serviceName + ".service"

// systemctlPath: /usr/bin nas distribuições com /usr unificado; /bin nas antigas.
func systemctlPath() string {
	for _, p := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// systemdReady confere que o systemd é o init e devolve o caminho do systemctl.
// /run/systemd/system só existe quando o systemd é o init (a mesma checagem do sd_booted).
func systemdReady() (string, error) {
	systemctl := systemctlPath()
	if _, err := os.Stat("/run/systemd/system"); err != nil || systemctl == "" {
		return "", errors.New("este sistema não usa o systemd; instalação não suportada")
	}
	return systemctl, nil
}

// serviceLog: no Linux, o stderr do serviço vai para o journald, que já guarda, rotaciona
// e indexa (journalctl -u patchd-agent).
func serviceLog(dataDir string, asService bool, stderr io.Writer) (io.Writer, func(), error) {
	return stderr, func() {}, nil
}

// serviceInstall copia o agente para /usr/local/sbin, protege a pasta de dados, grava a
// unidade do systemd e a habilita e inicia. As opções dadas aqui viram a linha de comando.
func serviceInstall(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	cfg, err := loadAgentConfig(args, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida: %v\n", err)
		return exitConfig
	}
	if why, refuse := containerCheck(allowContainer(look)); refuse {
		fmt.Fprintf(stderr, "patchd-agent: não instalo o serviço dentro de container (%s)\n", why)
		return exitConfig
	}
	systemctl, err := systemdReady()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	if _, err := credential.Load(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: registre a máquina antes de instalar o serviço (patchd-agent enroll -data-dir %s): %v\n", cfg.DataDir, err)
		return exitRuntime
	}
	if _, err := os.Stat(unitPath); err == nil {
		fmt.Fprintf(stderr, "patchd-agent: %s já existe; desinstale antes (patchd-agent service uninstall)\n", unitPath)
		return exitRuntime
	}

	if err := protectDataDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: proteger %s: %v\n", cfg.DataDir, err)
		return exitRuntime
	}
	if err := copyBinary(installedBinary); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: copiar o agente para %s: %v\n", installedBinary, err)
		return exitRuntime
	}
	if err := writeRootFile(unitPath, []byte(systemdUnit(installedBinary, args)), 0o644); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: gravar %s: %v\n", unitPath, err)
		return exitRuntime
	}
	if err := runTool(systemctl, "daemon-reload"); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: systemctl daemon-reload: %v\n", err)
		return exitRuntime
	}
	if err := runTool(systemctl, "enable", "--now", serviceName+".service"); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: systemctl enable --now: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "serviço %s instalado e iniciado\n  executável: %s\n  unidade: %s\n  log: journalctl -u %s\n",
		serviceName, installedBinary, unitPath, serviceName)
	return exitOK
}

// serviceUninstall para e remove o serviço, a unidade e o executável. A pasta de dados
// (com a credencial) fica: reinstalar não deve criar outra máquina no servidor.
func serviceUninstall(stdout, stderr io.Writer) int {
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	if _, err := os.Stat(unitPath); err != nil {
		fmt.Fprintf(stdout, "o serviço %s não está instalado\n", serviceName)
		return exitOK
	}
	systemctl := systemctlPath()
	if err := runTool(systemctl, "disable", "--now", serviceName+".service"); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: systemctl disable --now: %v\n", err)
	}
	if err := removeIfExists(unitPath); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: remover %s: %v\n", unitPath, err)
		return exitRuntime
	}
	if err := runTool(systemctl, "daemon-reload"); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: systemctl daemon-reload: %v\n", err)
	}
	if err := removeIfExists(installedBinary); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: não foi possível apagar %s: %v\n", installedBinary, err)
	}
	fmt.Fprintf(stdout, "serviço %s removido; a pasta de dados e a credencial foram mantidas\n", serviceName)
	return exitOK
}

// installedState compara a unidade e o executável instalados com os que este install
// gravaria: iguais, não há o que fazer; diferentes, o install reinstala.
func installedState(cfg agentConfig, args []string) (serviceState, error) {
	if _, err := systemdReady(); err != nil {
		return 0, err
	}
	unit, err := os.ReadFile(unitPath)
	if errors.Is(err, fs.ErrNotExist) {
		return serviceMissing, nil
	}
	if err != nil {
		return 0, err
	}
	same, err := sameExecutable(installedBinary)
	if err != nil {
		return 0, err
	}
	if same && string(unit) == systemdUnit(installedBinary, args) {
		return serviceCurrent, nil
	}
	return serviceOutdated, nil
}

// serviceStart: "enable --now" é idempotente; habilita e inicia só o que faltar (inclusive
// uma unidade em "failed").
func serviceStart() error {
	return runTool(systemctlPath(), "enable", "--now", serviceName+".service")
}
