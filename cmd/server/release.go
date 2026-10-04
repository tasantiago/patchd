package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/version"
)

const releaseUsage = `uso: patchd-server release <comando>

comandos:
  current            mostra a versão do agente publicada
  publish VERSÃO     confere a pasta da versão e passa a oferecê-la aos agentes
  withdraw           retira a oferta (os agentes ficam na versão em que estão)

A pasta vem de PATCHD_RELEASES_DIR. A versão é assinada antes, fora do servidor, com o
patchd-release; aqui só se confere a integridade dos arquivos.
`

// runRelease administra a versão do agente oferecida pelo servidor.
func runRelease(args []string, look config.Lookup, stdout, stderr io.Writer, serverVersion string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, releaseUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ReleasesDir == "" {
		fmt.Fprintln(stderr, "patchd-server: defina PATCHD_RELEASES_DIR")
		return exitConfig
	}
	dir := release.Dir(cfg.ReleasesDir)

	switch {
	case args[0] == "current" && len(args) == 1:
		v, err := dir.Current()
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if v == "" {
			fmt.Fprintln(stderr, "nenhuma versão publicada")
			return exitOK
		}
		fmt.Fprintln(stdout, v)
		return exitOK

	case args[0] == "publish" && len(args) == 2:
		v := args[1]
		// O servidor é atualizado antes dos agentes: um agente mais novo que o servidor
		// poderia falar uma versão do protocolo que o servidor ainda não entende.
		c, err := version.CompareSemver(v, serverVersion)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		if c > 0 {
			fmt.Fprintf(stderr, "patchd-server: o agente %s é mais novo que este servidor (%s); atualize o servidor antes\n", v, serverVersion)
			return exitConfig
		}
		m, err := dir.Check(v)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: a versão %s não pode ser publicada: %v\n", v, err)
			return exitRuntime
		}
		if err := dir.Publish(v); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "versão %s publicada (%d executáveis conferidos); os agentes a recebem no próximo check-in\n", v, len(m.Files))
		return exitOK

	case args[0] == "withdraw" && len(args) == 1:
		if err := dir.Withdraw(); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintln(stdout, "oferta retirada; nenhuma versão publicada")
		return exitOK

	default:
		fmt.Fprint(stderr, releaseUsage)
		return exitConfig
	}
}

// logReleases registra, na partida, o que o servidor vai oferecer.
func logReleases(logger *slog.Logger, dir release.Dir, serverVersion string) {
	v, err := dir.Current()
	switch {
	case err != nil:
		logger.Warn("versão publicada do agente ilegível", "error", err)
	case v == "":
		logger.Info("atualização do agente ligada; nenhuma versão publicada", "releases_dir", string(dir))
	default:
		c, err := version.CompareSemver(v, serverVersion)
		if err != nil || c > 0 {
			logger.Warn("versão publicada do agente mais nova que o servidor (ou fora do formato): não será oferecida",
				"published", v, "server", serverVersion)
			return
		}
		logger.Info("atualização do agente ligada", "releases_dir", string(dir), "published", v)
	}
}
