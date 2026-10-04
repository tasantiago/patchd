package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
)

const (
	plistPath     = "/Library/LaunchDaemons/" + launchdLabel + ".plist"
	launchctlPath = "/bin/launchctl"
)

// serviceLog: o launchd não rotaciona arquivos; o log vai para o arquivo rotativo da pasta
// de dados, como no Windows.
func serviceLog(dataDir string, asService bool, stderr io.Writer) (io.Writer, func(), error) {
	if !asService {
		return stderr, func() {}, nil
	}
	return rotatingLog(dataDir)
}

// serviceInstall copia o agente para /usr/local/sbin, protege a pasta de dados, grava o
// LaunchDaemon (root:wheel, 0644: o launchd recusa plist gravável por outros) e o carrega.
func serviceInstall(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	cfg, err := loadAgentConfig(args, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida: %v\n", err)
		return exitConfig
	}
	if _, err := credential.Load(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: registre a máquina antes de instalar o serviço (patchd-agent enroll -data-dir %q): %v\n", cfg.DataDir, err)
		return exitRuntime
	}
	if _, err := os.Stat(plistPath); err == nil {
		fmt.Fprintf(stderr, "patchd-agent: %s já existe; desinstale antes (patchd-agent service uninstall)\n", plistPath)
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
	if err := writeRootFile(plistPath, []byte(launchdPlist(installedBinary, args, cfg.DataDir)), 0o644); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: gravar %s: %v\n", plistPath, err)
		return exitRuntime
	}
	if err := runTool(launchctlPath, "bootstrap", "system", plistPath); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: launchctl bootstrap: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "serviço %s instalado e iniciado\n  executável: %s\n  plist: %s\n  log: %s/logs/agent.log\n",
		launchdLabel, installedBinary, plistPath, cfg.DataDir)
	return exitOK
}

// serviceUninstall descarrega e remove o LaunchDaemon e o executável. A pasta de dados
// (com a credencial) fica.
func serviceUninstall(stdout, stderr io.Writer) int {
	if !requireAdmin(stderr) {
		return exitRuntime
	}
	if _, err := os.Stat(plistPath); err != nil {
		fmt.Fprintf(stdout, "o serviço %s não está instalado\n", launchdLabel)
		return exitOK
	}
	// bootout envia SIGTERM e espera o ExitTimeOut. "No such process": já estava descarregado.
	if err := runTool(launchctlPath, "bootout", "system/"+launchdLabel); err != nil && !strings.Contains(err.Error(), "No such process") {
		fmt.Fprintf(stderr, "patchd-agent: aviso: launchctl bootout: %v\n", err)
	}
	if err := removeIfExists(plistPath); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: remover %s: %v\n", plistPath, err)
		return exitRuntime
	}
	if err := removeIfExists(installedBinary); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: não foi possível apagar %s: %v\n", installedBinary, err)
	}
	fmt.Fprintf(stdout, "serviço %s removido; a pasta de dados e a credencial foram mantidas\n", launchdLabel)
	return exitOK
}

// installedState compara o plist e o executável instalados com os que este install
// gravaria: iguais, não há o que fazer; diferentes, o install reinstala.
func installedState(cfg agentConfig, args []string) (serviceState, error) {
	plist, err := os.ReadFile(plistPath)
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
	if same && string(plist) == launchdPlist(installedBinary, args, cfg.DataDir) {
		return serviceCurrent, nil
	}
	return serviceOutdated, nil
}

// serviceStart: carregado, o kickstart (sem -k) inicia só se não estiver rodando;
// descarregado (um "bootout" manual, por exemplo), o bootstrap carrega e inicia.
func serviceStart() error {
	if runTool(launchctlPath, "print", "system/"+launchdLabel) != nil {
		return runTool(launchctlPath, "bootstrap", "system", plistPath)
	}
	return runTool(launchctlPath, "kickstart", "system/"+launchdLabel)
}
