#!/usr/bin/env bash
# Aula 5.4: cria os arquivos novos e aplica as alterações. Rode na raiz do repositório,
# com a árvore limpa no commit da Aula 5.3.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
if [ -n "$(git status --porcelain)" ]; then echo "ERRO: há alterações não commitadas; faça o commit da 5.3 antes." >&2; exit 1; fi

mkdir -p cmd/agent
cat > cmd/agent/update.go <<'PATCHD_EOF'
package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/release"
)

// newUpdater monta a atualização automática. Sem a chave pública embutida no build, ou com
// uma chave inválida, ela fica desligada, e o motivo vai para o log uma vez, na partida.
func newUpdater(logger *slog.Logger, client *agent.Client, opts loopOptions, current string) func(context.Context, string) {
	if release.PublicKey == "" {
		logger.Warn("atualização automática desligada: este executável foi compilado sem chave pública (PATCHD_UPDATE_PUBKEY)")
		return nil
	}
	pub, err := release.ParsePublicKey(release.PublicKey)
	if err != nil {
		logger.Error("atualização automática desligada", "error", err)
		return nil
	}
	u := &agent.Updater{
		Client:    client,
		DataDir:   opts.DataDir,
		Current:   current,
		PublicKey: pub,
		Install:   func(bin string) error { return startInstaller(bin, opts.ServiceArgs, opts.DataDir) },
		Logger:    logger,
	}
	u.CleanUpdates()
	return u.Offer
}
PATCHD_EOF
echo "criado: cmd/agent/update.go"

mkdir -p cmd/agent
cat > cmd/agent/update_darwin.go <<'PATCHD_EOF'
package main

import "syscall"

// detachedAttr: sessão própria (setsid). Ao descarregar o serviço, o launchd encerra o
// grupo de processos dele; numa sessão nova, o instalador fica fora desse grupo.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
PATCHD_EOF
echo "criado: cmd/agent/update_darwin.go"

mkdir -p cmd/agent
cat > cmd/agent/update_detached.go <<'PATCHD_EOF'
//go:build windows || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// startInstaller roda o install da versão nova num processo separado deste serviço, com a
// saída em logs/update.log. Ele para este serviço, troca o executável e inicia o novo;
// por isso não pode morrer junto com o serviço (detachedAttr, por SO).
func startInstaller(bin string, args []string, dataDir string) error {
	logPath := filepath.Join(dataDir, "logs", "update.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close() // o filho tem a própria cópia do arquivo aberto
	fmt.Fprintf(logf, "==== %s instalador: %s\n", time.Now().Format(time.RFC3339), bin)

	cmd := exec.Command(bin, append([]string{"install"}, args...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
PATCHD_EOF
echo "criado: cmd/agent/update_detached.go"

mkdir -p cmd/agent
cat > cmd/agent/update_linux.go <<'PATCHD_EOF'
package main

import (
	"errors"
	"os"
)

// startInstaller roda o install da versão nova numa unidade transitória do systemd. Um
// processo filho comum não serviria: ao parar o serviço, o systemd encerra todo o cgroup
// dele, inclusive o instalador que pediu a parada. A unidade transitória também fica fora
// do ProtectSystem=full do serviço, e pode gravar em /usr/local/sbin e /etc/systemd.
// A saída fica no journal: journalctl -u patchd-agent-update.
func startInstaller(bin string, args []string, dataDir string) error {
	systemdRun := ""
	for _, p := range []string{"/usr/bin/systemd-run", "/bin/systemd-run"} {
		if _, err := os.Stat(p); err == nil {
			systemdRun = p
			break
		}
	}
	if systemdRun == "" {
		return errors.New("systemd-run não encontrado")
	}
	cmd := append([]string{"--unit=" + serviceName + "-update", "--collect", "--quiet", "--", bin, "install"}, args...)
	return runTool(systemdRun, cmd...)
}
PATCHD_EOF
echo "criado: cmd/agent/update_linux.go"

mkdir -p cmd/agent
cat > cmd/agent/update_windows.go <<'PATCHD_EOF'
package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedAttr: sem console e num grupo de processos próprio. O SCM, ao parar o serviço,
// encerra só o processo do serviço; o instalador continua.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
PATCHD_EOF
echo "criado: cmd/agent/update_windows.go"

mkdir -p cmd/release
cat > cmd/release/main.go <<'PATCHD_EOF'
// Comando patchd-release: a ferramenta do operador para as versões do agente. Gera o par
// de chaves e assina o manifesto de uma versão. Roda fora do servidor, onde está a chave
// privada; o servidor só recebe a pasta já assinada.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tasantiago/patchd/internal/release"
)

const (
	exitOK      = 0
	exitRuntime = 1
	exitConfig  = 2
)

// targets são os executáveis do agente que o scripts/build.sh gera.
var targets = [][2]string{
	{"windows", "amd64"}, {"windows", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
}

const usage = `uso: patchd-release <comando> [opções]

comandos:
  keygen -key ARQUIVO                 gera o par de chaves; grava a privada e imprime a pública
  sign -key ARQUIVO -version VERSÃO -dist PASTA -out PASTA
                                      copia os executáveis do dist/ para PASTA/VERSÃO e assina o manifesto
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitConfig
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], stdout, stderr)
	case "sign":
		return sign(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "patchd-release: comando desconhecido %q\n\n%s", args[0], usage)
		return exitConfig
	}
}

// keygen grava a chave privada (0600, nunca por cima de uma existente) e imprime a
// pública, que vai para o PATCHD_UPDATE_PUBKEY do build.
func keygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "onde gravar a chave privada (fora do repositório)")
	if err := fs.Parse(args); err != nil || *keyPath == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitConfig
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	if err := os.MkdirAll(filepath.Dir(*keyPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	// O_EXCL: uma chave existente nunca é sobrescrita (perder a chave obriga a reinstalar
	// a frota inteira com um executável que confie na nova).
	f, err := os.OpenFile(*keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	if _, err := f.WriteString(release.EncodePrivateKey(priv)); err != nil {
		f.Close()
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "patchd-release: chave privada gravada em %s; guarde uma cópia offline\n", *keyPath)
	fmt.Fprintln(stdout, release.EncodePublicKey(pub))
	return exitOK
}

// sign monta a pasta da versão: os executáveis do dist/, o manifesto e a assinatura.
func sign(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "arquivo da chave privada")
	version := fs.String("version", "", "versão, igual à injetada no build (ex.: v0.5.1)")
	dist := fs.String("dist", "dist", "pasta com os executáveis do build")
	out := fs.String("out", "", "pasta de versões; a versão vai para PASTA/VERSÃO")
	if err := fs.Parse(args); err != nil || *keyPath == "" || *version == "" || *out == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitConfig
	}
	if !release.VersionPattern.MatchString(*version) {
		fmt.Fprintf(stderr, "patchd-release: versão %q inválida\n", *version)
		return exitConfig
	}
	priv, err := loadPrivateKey(*keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitConfig
	}

	// Primeiro lê tudo: sem nenhum executável no dist/, nada é criado.
	m := release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: *version, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	contents := map[string][]byte{}
	for _, t := range targets {
		name := "patchd-agent-" + t[0] + "-" + t[1]
		if t[0] == "windows" {
			name += ".exe"
		}
		data, err := os.ReadFile(filepath.Join(*dist, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(stderr, "patchd-release: %v\n", err)
			return exitRuntime
		}
		contents[name] = data
		m.Files = append(m.Files, release.File{OS: t[0], Arch: t[1], Name: name, SHA256: release.HashBytes(data), Size: int64(len(data))})
	}
	if len(m.Files) == 0 {
		fmt.Fprintf(stderr, "patchd-release: nenhum executável do agente em %s (rode o scripts/build.sh)\n", *dist)
		return exitConfig
	}

	vdir := filepath.Join(*out, *version)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	// Uma versão assinada não muda: mudar o conteúdo é publicar outra versão.
	if err := os.Mkdir(vdir, 0o755); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v (uma versão assinada não é refeita; use outro número)\n", err)
		return exitRuntime
	}
	for name, data := range contents {
		if err := os.WriteFile(filepath.Join(vdir, name), data, 0o644); err != nil {
			fmt.Fprintf(stderr, "patchd-release: %v\n", err)
			return exitRuntime
		}
	}
	manifest, sig, err := release.Sign(priv, m)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	if err := os.WriteFile(filepath.Join(vdir, release.ManifestFile), manifest, 0o644); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	if err := os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644); err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "versão %s assinada em %s (%d executáveis)\n", *version, vdir, len(m.Files))
	return exitOK
}

// loadPrivateKey lê a chave privada e recusa um arquivo que outros usuários possam ler.
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s pode ser lida por outros usuários (%s); use chmod 600", path, fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return release.ParsePrivateKey(string(data))
}
PATCHD_EOF
echo "criado: cmd/release/main.go"

mkdir -p cmd/release
cat > cmd/release/main_test.go <<'PATCHD_EOF'
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/release"
)

func TestKeygenEAssinatura(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "chaves", "release.key")
	var out, errs bytes.Buffer
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errs.String())
	}
	pub, err := release.ParsePublicKey(out.String())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissão da chave: %s", fi.Mode().Perm())
	}
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code == exitOK {
		t.Error("keygen não pode sobrescrever uma chave existente")
	}

	dist := filepath.Join(dir, "dist")
	_ = os.MkdirAll(dist, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-linux-amd64"), []byte("agente linux"), 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-windows-amd64.exe"), []byte("agente windows"), 0o755)
	rel := filepath.Join(dir, "releases")

	out.Reset()
	args := []string{"sign", "-key", key, "-version", "v0.5.1", "-dist", dist, "-out", rel}
	if code := run(args, &out, &errs); code != exitOK || !strings.Contains(out.String(), "2 executáveis") {
		t.Fatalf("sign: %d %s %s", code, out.String(), errs.String())
	}
	data, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.ManifestFile))
	sig, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.SignatureFile))
	m, err := release.Verify(pub, data, sig)
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("o manifesto assinado confere com a chave pública: %+v %v", m, err)
	}
	if _, err := release.Dir(rel).Check("v0.5.1"); err != nil {
		t.Errorf("a pasta gerada passa na conferência do servidor: %v", err)
	}

	// A mesma versão não é assinada de novo.
	if code := run(args, &out, &errs); code == exitOK {
		t.Error("versão já assinada deveria ser recusada")
	}

	// Chave legível por outros: recusada.
	_ = os.Chmod(key, 0o644)
	args[6] = "v0.5.2"
	errs.Reset()
	if code := run(args, &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "chmod 600") {
		t.Errorf("chave aberta: %d %s", code, errs.String())
	}
}
PATCHD_EOF
echo "criado: cmd/release/main_test.go"

mkdir -p cmd/server
cat > cmd/server/release.go <<'PATCHD_EOF'
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
PATCHD_EOF
echo "criado: cmd/server/release.go"

mkdir -p cmd/server
cat > cmd/server/release_test.go <<'PATCHD_EOF'
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/release"
)

func TestReleasePublicaSoAgenteNaoMaisNovoQueOServidor(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	for _, v := range []string{"v0.5.0", "v0.5.1"} {
		conteudo := []byte("agente " + v)
		vdir := filepath.Join(dir, v)
		_ = os.MkdirAll(vdir, 0o755)
		data, sig, err := release.Sign(priv, release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: v,
			CreatedAt: time.Now().UTC(), Files: []release.File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
				SHA256: release.HashBytes(conteudo), Size: int64(len(conteudo))}}})
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(vdir, release.ManifestFile), data, 0o644)
		_ = os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644)
		_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)
	}
	amb := func(k string) (string, bool) {
		if k == "PATCHD_RELEASES_DIR" {
			return dir, true
		}
		return "", false
	}

	var out, errs bytes.Buffer
	// Servidor v0.5.0: o agente v0.5.1 é recusado; o v0.5.0, aceito.
	if code := runRelease([]string{"publish", "v0.5.1"}, amb, &out, &errs, "v0.5.0"); code != exitConfig || !strings.Contains(errs.String(), "atualize o servidor antes") {
		t.Errorf("agente mais novo que o servidor: %d %s", code, errs.String())
	}
	if code := runRelease([]string{"publish", "v0.5.0"}, amb, &out, &errs, "v0.5.0"); code != exitOK {
		t.Fatalf("publicar v0.5.0: %d %s", code, errs.String())
	}
	// Servidor "dev" (sem versão de verdade): nada é publicado.
	errs.Reset()
	if code := runRelease([]string{"publish", "v0.5.0"}, amb, &out, &errs, "dev"); code != exitConfig {
		t.Errorf("servidor sem versão: %d %s", code, errs.String())
	}

	out.Reset()
	if code := runRelease([]string{"current"}, amb, &out, &errs, "v0.5.0"); code != exitOK || strings.TrimSpace(out.String()) != "v0.5.0" {
		t.Errorf("current: %d %q", code, out.String())
	}
	if code := runRelease([]string{"withdraw"}, amb, &out, &errs, "v0.5.0"); code != exitOK {
		t.Errorf("withdraw: %d", code)
	}
	if v, _ := release.Dir(dir).Current(); v != "" {
		t.Errorf("depois do withdraw: %q", v)
	}
}
PATCHD_EOF
echo "criado: cmd/server/release_test.go"

mkdir -p internal/agent
cat > internal/agent/download.go <<'PATCHD_EOF'
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Download baixa um arquivo (GET, com a credencial da máquina) para w. Recusa respostas
// maiores que limit: um servidor comprometido não consegue encher o disco da máquina.
func (c *Client) Download(ctx context.Context, path string, w io.Writer, limit int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, err
	}
	requestID := newRequestID()
	req.Header.Set("Authorization", "Bearer "+c.Credential)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "patchd-agent/"+c.AgentVersion)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &TransientError{Err: err, RequestID: requestID}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return 0, ErrUnauthorized
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests:
		return 0, &TransientError{Err: fmt.Errorf("o servidor respondeu %d", resp.StatusCode), RequestID: requestID}
	default:
		var e protocol.ErrorResponse
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = json.Unmarshal(data, &e)
		return 0, &RejectedError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RequestID: requestID}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, &TransientError{Err: err, RequestID: requestID}
	}
	if n > limit {
		return n, fmt.Errorf("%s: resposta maior que o limite de %d bytes", path, limit)
	}
	return n, nil
}
PATCHD_EOF
echo "criado: internal/agent/download.go"

mkdir -p internal/agent
cat > internal/agent/update.go <<'PATCHD_EOF'
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/version"
)

const (
	updateDir        = "update"       // dentro da pasta de dados: protegida, só SYSTEM/root
	attemptFile      = "attempt.json" // última tentativa: evita repetir uma atualização que falha
	updateRetryAfter = 6 * time.Hour
	selfTestTimeout  = 30 * time.Second
	maxSignatureSize = 4 << 10
)

// Updater baixa uma versão nova do agente, confere tudo e entrega o executável ao
// instalador. A ordem importa: a assinatura do manifesto é conferida antes de qualquer
// outra coisa; o executável só é executado depois de o SHA-256 dele bater com o manifesto
// assinado; e ele fica na pasta de dados, onde um usuário comum não consegue trocá-lo
// entre a conferência e a execução.
type Updater struct {
	Client    *Client
	DataDir   string
	Current   string            // versão deste executável
	PublicKey ed25519.PublicKey // a chave embutida no build
	// Install inicia a instalação da versão nova fora deste processo: ela vai parar este
	// serviço, trocar o executável e iniciar o novo.
	Install func(bin string) error
	Logger  *slog.Logger

	// Injetáveis nos testes.
	now          func() time.Time
	selfTest     func(ctx context.Context, bin string) (string, error)
	goos, goarch string
}

type updateAttempt struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

func (u *Updater) defaults() {
	if u.now == nil {
		u.now = time.Now
	}
	if u.selfTest == nil {
		u.selfTest = runVersion
	}
	if u.goos == "" {
		u.goos, u.goarch = runtime.GOOS, runtime.GOARCH
	}
}

// Offer trata a versão oferecida pelo servidor no check-in.
func (u *Updater) Offer(ctx context.Context, offered string) {
	u.defaults()
	c, err := version.CompareSemver(offered, u.Current)
	if err != nil {
		u.Logger.Warn("atualização oferecida ignorada: versão fora do formato", "offered", offered, "current", u.Current, "error", err)
		return
	}
	if c <= 0 {
		// Uma versão igual ou mais velha nunca é instalada: um manifesto antigo, assinado e
		// com uma falha conhecida, não pode ser reaproveitado para "voltar" a frota. Voltar
		// atrás é publicar uma versão nova com o código antigo.
		u.Logger.Warn("atualização oferecida recusada: não é mais nova que a instalada", "offered", offered, "current", u.Current)
		return
	}

	path := filepath.Join(u.DataDir, updateDir, attemptFile)
	var last updateAttempt
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &last)
	}
	if last.Version == offered && u.now().Sub(last.At) < updateRetryAfter {
		u.Logger.Debug("atualização já tentada há pouco; aguardando", "offered", offered, "last_attempt", last.At, "last_error", last.Error)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		u.Logger.Error("atualização: pasta de download", "error", err)
		return
	}
	// A tentativa é registrada antes: se o processo cair no meio, não tenta de novo a cada ciclo.
	attempt := updateAttempt{Version: offered, At: u.now().UTC()}
	_ = writeJSONAtomic(path, attempt)

	u.Logger.Info("atualização do agente iniciada", "from", u.Current, "to", offered)
	bin, err := u.fetch(ctx, offered)
	if err == nil {
		u.Logger.Info("versão nova baixada e conferida; iniciando o instalador", "target", offered, "file", bin)
		err = u.Install(bin)
	}
	if err != nil {
		attempt.Error = err.Error()
		_ = writeJSONAtomic(path, attempt)
		u.Logger.Error("atualização do agente falhou; nova tentativa em "+updateRetryAfter.String(), "target", offered, "error", err)
	}
}

// fetch baixa e confere o manifesto e o executável; devolve o caminho do executável.
func (u *Updater) fetch(ctx context.Context, v string) (string, error) {
	base := "/api/v1/agent/releases/" + v + "/"
	var mbuf, sbuf bytes.Buffer
	if _, err := u.Client.Download(ctx, base+release.ManifestFile, &mbuf, release.MaxManifestSize); err != nil {
		return "", fmt.Errorf("baixar o manifesto: %w", err)
	}
	if _, err := u.Client.Download(ctx, base+release.SignatureFile, &sbuf, maxSignatureSize); err != nil {
		return "", fmt.Errorf("baixar a assinatura: %w", err)
	}
	m, err := release.Verify(u.PublicKey, mbuf.Bytes(), sbuf.Bytes())
	if err != nil {
		return "", err
	}
	// O manifesto assinado precisa ser o da versão pedida: senão, um servidor poderia
	// entregar o manifesto (legítimo) de outra versão.
	if m.Version != v {
		return "", fmt.Errorf("o manifesto assinado é da versão %s, não da %s", m.Version, v)
	}
	f, ok := m.For(u.goos, u.goarch)
	if !ok {
		return "", fmt.Errorf("a versão %s não tem executável para %s/%s", v, u.goos, u.goarch)
	}

	final := filepath.Join(u.DataDir, updateDir, v+"-"+f.Name)
	part := final + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := u.Client.Download(ctx, base+f.Name, io.MultiWriter(out, h), f.Size)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256) {
		err = fmt.Errorf("%s não confere com o manifesto assinado (tamanho ou SHA-256)", f.Name)
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return "", err
	}

	// Por último, o executável precisa rodar nesta máquina e dizer a versão certa.
	got, err := u.selfTest(ctx, final)
	if err != nil || !strings.HasPrefix(got, "patchd-agent "+v+" ") {
		os.Remove(final)
		return "", fmt.Errorf("o executável novo não se identificou como %s (%q, %v)", v, strings.TrimSpace(got), err)
	}
	return final, nil
}

// CleanUpdates apaga executáveis de atualizações anteriores (o registro da última
// tentativa fica). Um arquivo ainda em uso (o instalador terminando) fica para a próxima.
func (u *Updater) CleanUpdates() {
	entries, err := os.ReadDir(filepath.Join(u.DataDir, updateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	for _, e := range entries {
		if e.Name() != attemptFile {
			_ = os.Remove(filepath.Join(u.DataDir, updateDir, e.Name()))
		}
	}
}

// runVersion executa "<bin> -version" com prazo.
func runVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, selfTestTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").Output()
	return string(out), err
}
PATCHD_EOF
echo "criado: internal/agent/update.go"

mkdir -p internal/agent
cat > internal/agent/update_test.go <<'PATCHD_EOF'
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/release"
)

// versaoPublicada monta e publica, numa pasta de versões, a versão v com um executável
// linux/amd64 de conteúdo conteudo, assinada com priv.
func versaoPublicada(t *testing.T, priv ed25519.PrivateKey, v string, conteudo []byte) release.Dir {
	t.Helper()
	d := release.Dir(t.TempDir())
	vdir := filepath.Join(string(d), v)
	_ = os.MkdirAll(vdir, 0o755)
	m := release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: v, CreatedAt: time.Now().UTC(),
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
			SHA256: release.HashBytes(conteudo), Size: int64(len(conteudo))}}}
	data, sig, err := release.Sign(priv, m)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(vdir, release.ManifestFile), data, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)
	if err := d.Publish(v); err != nil {
		t.Fatal(err)
	}
	return d
}

type instalacoes struct {
	n        atomic.Int32
	conteudo []byte
	caminho  string
}

func updaterDeTeste(t *testing.T, srv *servidor, cred string, pub ed25519.PublicKey, atual string, inst *instalacoes, log io.Writer) *Updater {
	t.Helper()
	return &Updater{
		Client:    NewClient(srv.URL, cred, atual),
		DataDir:   t.TempDir(),
		Current:   atual,
		PublicKey: pub,
		Install: func(bin string) error {
			inst.n.Add(1)
			inst.caminho = bin
			inst.conteudo, _ = os.ReadFile(bin)
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(log, nil)),
		goos:   "linux",
		goarch: "amd64",
		selfTest: func(_ context.Context, bin string) (string, error) {
			return "patchd-agent v0.5.1 (commit teste)\n", nil
		},
	}
}

func TestAtualizacaoPeloCheckin(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	novo := []byte("executável v0.5.1")
	d := versaoPublicada(t, priv, "v0.5.1", novo)
	srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.1"))

	var inst instalacoes
	var log bytes.Buffer
	u := updaterDeTeste(t, srv, cred, pub, "v0.5.0", &inst, &log)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	a.Client = NewClient(srv.URL, cred, "v0.5.0")
	a.Update = u.Offer

	if out := a.CheckIn(context.Background()); out != outcomeOK {
		t.Fatalf("ciclo: %v", out)
	}
	if inst.n.Load() != 1 || !bytes.Equal(inst.conteudo, novo) {
		t.Fatalf("o instalador recebe o executável conferido: %d %q\n%s", inst.n.Load(), inst.conteudo, log.String())
	}
	if filepath.Dir(inst.caminho) != filepath.Join(u.DataDir, updateDir) {
		t.Errorf("o executável fica na pasta de dados: %s", inst.caminho)
	}

	// No ciclo seguinte (o serviço ainda não foi trocado), a mesma versão não é baixada de novo.
	if out := a.CheckIn(context.Background()); out != outcomeOK || inst.n.Load() != 1 {
		t.Errorf("tentativa recente não se repete: %v %d", out, inst.n.Load())
	}
	if srv.conta("/api/v1/agent/releases/v0.5.1/patchd-agent-linux-amd64") != 1 {
		t.Errorf("um download só: %d", srv.conta("/api/v1/agent/releases/v0.5.1/patchd-agent-linux-amd64"))
	}

	u.CleanUpdates()
	if _, err := os.Stat(inst.caminho); !errors.Is(err, os.ErrNotExist) {
		t.Error("a limpeza apaga o executável baixado")
	}
	if _, err := os.Stat(filepath.Join(u.DataDir, updateDir, attemptFile)); err != nil {
		t.Error("a limpeza mantém o registro da tentativa")
	}
}

func TestAtualizacaoRecusada(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	novo := []byte("executável v0.5.1")

	casos := []struct {
		nome    string
		estraga func(d release.Dir)
		atual   string
		chave   ed25519.PublicKey
		teste   func(context.Context, string) (string, error)
		quer    string
	}{
		{nome: "manifesto alterado no servidor", atual: "v0.5.0", chave: pub, quer: "assinatura",
			estraga: func(d release.Dir) {
				p := filepath.Join(string(d), "v0.5.1", release.ManifestFile)
				data, _ := os.ReadFile(p)
				_ = os.WriteFile(p, bytes.Replace(data, []byte(`"linux"`), []byte(`"linux" `), 1), 0o644)
			}},
		{nome: "executável trocado no servidor", atual: "v0.5.0", chave: pub, quer: "não confere",
			estraga: func(d release.Dir) { // mesmo tamanho, outro conteúdo
				_ = os.WriteFile(filepath.Join(string(d), "v0.5.1", "patchd-agent-linux-amd64"), []byte("executável v6.6.6"), 0o644)
			}},
		{nome: "executável maior que o do manifesto", atual: "v0.5.0", chave: pub, quer: "limite",
			estraga: func(d release.Dir) {
				_ = os.WriteFile(filepath.Join(string(d), "v0.5.1", "patchd-agent-linux-amd64"), bytes.Repeat([]byte("x"), 1<<20), 0o644)
			}},
		{nome: "outra chave", atual: "v0.5.0", quer: "assinatura",
			chave: func() ed25519.PublicKey { p, _, _ := ed25519.GenerateKey(rand.Reader); return p }()},
		{nome: "versão mais velha (rollback ou replay)", atual: "v0.6.0", chave: pub, quer: "não é mais nova"},
		{nome: "mesma versão", atual: "v0.5.1", chave: pub, quer: "não é mais nova"},
		{nome: "executável não se identifica", atual: "v0.5.0", chave: pub, quer: "não se identificou",
			teste: func(context.Context, string) (string, error) { return "patchd-agent v0.4.0 (x)", nil }},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			d := versaoPublicada(t, priv, "v0.5.1", novo)
			if c.estraga != nil {
				c.estraga(d)
			}
			srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.1"))
			var inst instalacoes
			var log bytes.Buffer
			u := updaterDeTeste(t, srv, cred, c.chave, c.atual, &inst, &log)
			if c.teste != nil {
				u.selfTest = c.teste
			}
			u.Offer(context.Background(), "v0.5.1")
			if inst.n.Load() != 0 || !strings.Contains(log.String(), c.quer) {
				t.Errorf("nada instalado, e o log diz %q: instalações=%d\n%s", c.quer, inst.n.Load(), log.String())
			}
			// Nenhum executável recusado fica no disco.
			entries, _ := os.ReadDir(filepath.Join(u.DataDir, updateDir))
			for _, e := range entries {
				if e.Name() != attemptFile {
					t.Errorf("sobrou %s na pasta de atualização", e.Name())
				}
			}
		})
	}
}

func TestServidorNaoOfereceAgenteMaisNovoQueEle(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := versaoPublicada(t, priv, "v0.5.1", []byte("x"))

	// Servidor v0.5.0 com o agente v0.5.1 publicado: nada é oferecido nem servido.
	srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.0"))
	c := NewClient(srv.URL, cred, "v0.5.0")
	resp, err := c.CheckIn(context.Background(), "")
	if err != nil || resp.AgentUpdate != nil {
		t.Errorf("servidor mais velho que a versão publicada não oferece: %+v %v", resp, err)
	}
	var rej *RejectedError
	if _, err := c.Download(context.Background(), "/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 1<<20); !errors.As(err, &rej) || rej.Status != 404 {
		t.Errorf("nem serve os arquivos: %v", err)
	}

	// Servidor atualizado: oferece a quem está em outra versão, e não a quem já está nela.
	srv2, _, cred2 := novoServidor(t, api.WithReleases(d, "v0.5.1"))
	resp, _ = NewClient(srv2.URL, cred2, "v0.5.0").CheckIn(context.Background(), "")
	if resp.AgentUpdate == nil || resp.AgentUpdate.Version != "v0.5.1" {
		t.Errorf("oferta: %+v", resp.AgentUpdate)
	}
	resp, _ = NewClient(srv2.URL, cred2, "v0.5.1").CheckIn(context.Background(), "")
	if resp.AgentUpdate != nil {
		t.Errorf("quem já está na versão não recebe oferta: %+v", resp.AgentUpdate)
	}

	// Sem credencial, nada é baixado.
	if _, err := NewClient(srv2.URL, "patchd_mac_invalida", "v0.5.0").Download(context.Background(),
		"/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 1<<20); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("download sem credencial: %v", err)
	}
	// Download maior que o limite: recusado.
	if _, err := NewClient(srv2.URL, cred2, "v0.5.0").Download(context.Background(),
		"/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 10); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
PATCHD_EOF
echo "criado: internal/agent/update_test.go"

mkdir -p internal/api
cat > internal/api/releases.go <<'PATCHD_EOF'
package api

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/version"
)

// Option ajusta a API na criação.
type Option func(*api)

// WithReleases liga a atualização automática do agente (Aula 5.4): o check-in passa a
// oferecer a versão publicada na pasta, e os arquivos dela ficam disponíveis para as
// máquinas autenticadas. serverVersion é a versão deste servidor: uma versão do agente
// mais nova que o servidor nunca é oferecida (o servidor é atualizado antes dos agentes).
func WithReleases(dir release.Dir, serverVersion string) Option {
	return func(a *api) {
		a.releases = dir
		a.serverVersion = serverVersion
	}
}

// offeredVersion é a versão publicada que pode ser oferecida agora; "" quando nenhuma.
func (a *api) offeredVersion(r *http.Request) string {
	if a.releases == "" {
		return ""
	}
	v, err := a.releases.Current()
	if err != nil {
		a.logger.Warn("versão publicada do agente ilegível", "error", err, "request_id", RequestID(r.Context()))
		return ""
	}
	if v == "" || !agentNotNewerThanServer(v, a.serverVersion) {
		return ""
	}
	return v
}

// agentNotNewerThanServer: versões que não seguem o formato (um servidor "dev", por
// exemplo) nunca liberam a oferta.
func agentNotNewerThanServer(agent, server string) bool {
	c, err := version.CompareSemver(agent, server)
	return err == nil && c <= 0
}

// releaseFile entrega um arquivo da versão publicada: o manifesto, a assinatura ou um
// executável. Outras versões (não publicadas ou retiradas) não são servidas.
func (a *api) releaseFile(w http.ResponseWriter, r *http.Request, id string) {
	v, name := r.PathValue("version"), r.PathValue("file")
	if v == "" || v != a.offeredVersion(r) {
		writeError(w, http.StatusNotFound, "release_not_found", "versão não publicada")
		return
	}
	f, err := a.releases.Open(v, name)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "release_not_found", "arquivo não encontrado na versão")
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir arquivo da versão", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "release_not_found", "arquivo não encontrado na versão")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	a.logger.Info("download de versão do agente", "machine_id", id, "release", v, "file", name, "request_id", RequestID(r.Context()))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
PATCHD_EOF
echo "criado: internal/api/releases.go"

mkdir -p internal/release
cat > internal/release/dir.go <<'PATCHD_EOF'
package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// currentFile guarda, dentro da pasta de versões, o nome da versão publicada.
const currentFile = "current"

// Dir é a pasta de versões do servidor:
//
//	<pasta>/<versão>/manifest.json, manifest.sig e os executáveis
//	<pasta>/current                     a versão oferecida aos agentes
//
// O servidor não tem a chave privada nem precisa da pública: ele confere só a integridade
// dos arquivos (SHA-256 e tamanho) ao publicar. Quem confere a assinatura é o agente.
type Dir string

// Current devolve a versão publicada; "" quando nenhuma está.
func (d Dir) Current() (string, error) {
	data, err := os.ReadFile(filepath.Join(string(d), currentFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(data))
	if !VersionPattern.MatchString(v) {
		return "", fmt.Errorf("%s contém uma versão inválida: %q", currentFile, v)
	}
	return v, nil
}

// Check lê o manifesto da versão e confere cada executável contra ele.
func (d Dir) Check(version string) (Manifest, error) {
	var m Manifest
	if !VersionPattern.MatchString(version) {
		return m, fmt.Errorf("versão %q inválida", version)
	}
	vdir := filepath.Join(string(d), version)
	data, err := os.ReadFile(filepath.Join(vdir, ManifestFile))
	if err != nil {
		return m, err
	}
	if _, err := os.Stat(filepath.Join(vdir, SignatureFile)); err != nil {
		return m, fmt.Errorf("a versão %s não tem %s: %w", version, SignatureFile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("%s ilegível: %w", ManifestFile, err)
	}
	if err := m.Check(); err != nil {
		return m, err
	}
	if m.Version != version {
		return m, fmt.Errorf("a pasta %s contém o manifesto da versão %s", version, m.Version)
	}
	for _, f := range m.Files {
		size, sum, err := hashFile(filepath.Join(vdir, f.Name))
		if err != nil {
			return m, err
		}
		if size != f.Size || sum != f.SHA256 {
			return m, fmt.Errorf("%s não confere com o manifesto (tamanho ou SHA-256)", f.Name)
		}
	}
	return m, nil
}

// Publish torna a versão a oferecida aos agentes, depois de conferi-la.
func (d Dir) Publish(version string) error {
	if _, err := d.Check(version); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(string(d), currentFile), []byte(version+"\n"))
}

// Withdraw retira a oferta: os agentes ficam na versão em que estão.
func (d Dir) Withdraw() error {
	err := os.Remove(filepath.Join(string(d), currentFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Open abre um arquivo de uma versão, para download. Os nomes são validados contra os
// padrões: nada de barras, "..", ou arquivos ocultos.
func (d Dir) Open(version, name string) (*os.File, error) {
	if !VersionPattern.MatchString(version) || !NamePattern.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	return os.Open(filepath.Join(string(d), version, name))
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // sem efeito depois da renomeação
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
PATCHD_EOF
echo "criado: internal/release/dir.go"

mkdir -p internal/release
cat > internal/release/release.go <<'PATCHD_EOF'
// Package release é a atualização do agente (Aula 5.4): o manifesto de uma versão, a
// assinatura Ed25519 que o protege e a pasta de versões que o servidor distribui.
//
// Quem assina é o operador, com a chave privada fora do servidor. O servidor só guarda e
// entrega os arquivos; quem confere a assinatura é o agente, com a chave pública embutida
// no executável. Um servidor invadido, portanto, não consegue fazer a frota rodar um
// executável que o operador não assinou.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PublicKey é a chave pública de atualização, em base64, injetada no build com
//
//	-ldflags "-X github.com/tasantiago/patchd/internal/release.PublicKey=..."
//
// Vazia (builds de desenvolvimento sem PATCHD_UPDATE_PUBKEY): o agente não se atualiza.
var PublicKey = ""

const (
	// ManifestSchemaVersion é a versão do formato do manifesto.
	ManifestSchemaVersion = 1
	// ManifestFile e SignatureFile são os nomes dos arquivos dentro da pasta da versão.
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.sig"
	// MaxManifestSize limita o que o agente aceita baixar como manifesto.
	MaxManifestSize = 1 << 20
	// PrivateKeyPrefix identifica o arquivo da chave privada.
	PrivateKeyPrefix = "patchd_relkey_"
)

// signingContext entra na mensagem assinada antes do manifesto: uma assinatura feita com
// a mesma chave para outra finalidade nunca vale como assinatura de manifesto.
const signingContext = "patchd-agent-manifest-v1\n"

// Manifest descreve uma versão do agente: os executáveis por SO e arquitetura, com o
// SHA-256 e o tamanho de cada um.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Version       string    `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	Files         []File    `json:"files"`
}

// File é um executável da versão.
type File struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"` // hexadecimal
	Size   int64  `json:"size"`
}

// For devolve o executável de um SO e arquitetura.
func (m Manifest) For(goos, goarch string) (File, bool) {
	for _, f := range m.Files {
		if f.OS == goos && f.Arch == goarch {
			return f, true
		}
	}
	return File{}, false
}

var (
	// VersionPattern e NamePattern valem para nomes de pasta e de arquivo: nada de barras,
	// "..", nem nomes começando com ponto.
	VersionPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z.+-]{0,63}$`)
	NamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Check confere a estrutura do manifesto (não a assinatura).
func (m Manifest) Check() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("manifesto com schema_version %d (esperado %d)", m.SchemaVersion, ManifestSchemaVersion)
	}
	if !VersionPattern.MatchString(m.Version) {
		return fmt.Errorf("versão %q inválida no manifesto", m.Version)
	}
	if len(m.Files) == 0 {
		return errors.New("manifesto sem executáveis")
	}
	for _, f := range m.Files {
		if f.OS == "" || f.Arch == "" || !NamePattern.MatchString(f.Name) || !sha256Pattern.MatchString(f.SHA256) || f.Size <= 0 {
			return fmt.Errorf("entrada inválida no manifesto: %+v", f)
		}
	}
	return nil
}

// Sign serializa o manifesto e o assina. Devolve os bytes exatos do manifest.json e o
// conteúdo do manifest.sig (a assinatura em base64).
func Sign(priv ed25519.PrivateKey, m Manifest) (manifest, sig []byte, err error) {
	if err := m.Check(); err != nil {
		return nil, nil, err
	}
	manifest, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	manifest = append(manifest, '\n')
	s := ed25519.Sign(priv, signedMessage(manifest))
	return manifest, []byte(base64.StdEncoding.EncodeToString(s) + "\n"), nil
}

// Verify confere a assinatura sobre os bytes exatos do manifesto e só então o interpreta.
// Nada do conteúdo é usado antes de a assinatura passar.
func Verify(pub ed25519.PublicKey, manifest, sig []byte) (Manifest, error) {
	var m Manifest
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return m, errors.New("assinatura do manifesto ilegível")
	}
	if !ed25519.Verify(pub, signedMessage(manifest), raw) {
		return m, errors.New("assinatura do manifesto inválida (outra chave ou manifesto alterado)")
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifesto assinado, mas ilegível: %w", err)
	}
	return m, m.Check()
}

func signedMessage(manifest []byte) []byte {
	return append([]byte(signingContext), manifest...)
}

// ParsePublicKey lê a chave pública em base64.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("chave pública de atualização inválida (esperado base64 de 32 bytes)")
	}
	return ed25519.PublicKey(raw), nil
}

// EncodePrivateKey e ParsePrivateKey: o arquivo da chave privada guarda a semente de 32
// bytes, em base64 URL, com o prefixo patchd_relkey_.
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return PrivateKeyPrefix + base64.RawURLEncoding.EncodeToString(priv.Seed()) + "\n"
}

func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), PrivateKeyPrefix)
	seed, err := base64.RawURLEncoding.DecodeString(rest)
	if !ok || err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("arquivo de chave privada inválido")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncodePublicKey é o formato do PATCHD_UPDATE_PUBKEY.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// HashBytes é o SHA-256 em hexadecimal, o formato do manifesto.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
PATCHD_EOF
echo "criado: internal/release/release.go"

mkdir -p internal/release
cat > internal/release/release_test.go <<'PATCHD_EOF'
package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func manifestoDeTeste(conteudo []byte) Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Version:       "v0.5.1",
		CreatedAt:     time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Files: []File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
			SHA256: HashBytes(conteudo), Size: int64(len(conteudo))}},
	}
}

func TestAssinaEConfere(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data, sig, err := Sign(priv, manifestoDeTeste([]byte("agente")))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Verify(pub, data, sig)
	if err != nil || m.Version != "v0.5.1" {
		t.Fatalf("manifesto íntegro: %+v %v", m, err)
	}
	if f, ok := m.For("linux", "amd64"); !ok || f.Name != "patchd-agent-linux-amd64" {
		t.Errorf("For: %+v %t", f, ok)
	}

	// Um byte mudado no manifesto (por exemplo, o SHA-256 de outro executável): recusado.
	alterado := []byte(strings.Replace(string(data), "v0.5.1", "v0.5.9", 1))
	if _, err := Verify(pub, alterado, sig); err == nil || !strings.Contains(err.Error(), "inválida") {
		t.Errorf("manifesto alterado: %v", err)
	}
	// Outra chave: recusado.
	outra, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(outra, data, sig); err == nil {
		t.Error("chave errada deveria ser recusada")
	}
	// Assinatura dos mesmos bytes sem o contexto de manifesto: recusada.
	semContexto := ed25519.Sign(priv, data)
	if _, err := Verify(pub, data, []byte(base64.StdEncoding.EncodeToString(semContexto))); err == nil {
		t.Error("assinatura sem o contexto deveria ser recusada")
	}
	if _, err := Verify(pub, data, []byte("lixo")); err == nil {
		t.Error("assinatura ilegível deveria ser recusada")
	}
}

func TestChaves(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p2, err := ParsePrivateKey(EncodePrivateKey(priv))
	if err != nil || !p2.Equal(priv) {
		t.Fatalf("chave privada: %v", err)
	}
	pub2, err := ParsePublicKey(EncodePublicKey(pub))
	if err != nil || !pub2.Equal(pub) {
		t.Fatalf("chave pública: %v", err)
	}
	for _, ruim := range []string{"", "abc", EncodePublicKey(pub)[:20]} {
		if _, err := ParsePublicKey(ruim); err == nil {
			t.Errorf("chave pública %q deveria ser recusada", ruim)
		}
	}
	if _, err := ParsePrivateKey(EncodePublicKey(pub)); err == nil {
		t.Error("chave pública não serve como privada")
	}
}

func TestDirPublica(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := Dir(t.TempDir())
	if v, err := d.Current(); err != nil || v != "" {
		t.Fatalf("sem publicação: %q %v", v, err)
	}

	conteudo := []byte("agente v0.5.1")
	vdir := filepath.Join(string(d), "v0.5.1")
	_ = os.MkdirAll(vdir, 0o755)
	data, sig, _ := Sign(priv, manifestoDeTeste(conteudo))
	_ = os.WriteFile(filepath.Join(vdir, ManifestFile), data, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, SignatureFile), sig, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)

	if err := d.Publish("v0.5.1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Current(); v != "v0.5.1" {
		t.Errorf("publicada: %q", v)
	}

	// Executável trocado depois de assinado: a publicação recusa.
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), []byte("outro"), 0o644)
	if err := d.Publish("v0.5.1"); err == nil || !strings.Contains(err.Error(), "não confere") {
		t.Errorf("executável trocado: %v", err)
	}
	if err := d.Publish("v9.9.9"); err == nil {
		t.Error("versão inexistente deveria ser recusada")
	}

	// Nomes perigosos nunca abrem nada.
	for _, c := range [][2]string{{"..", "x"}, {"v0.5.1", "../current"}, {"v0.5.1", ".oculto"}, {"v0.5.1/..", ManifestFile}} {
		if f, err := d.Open(c[0], c[1]); err == nil {
			f.Close()
			t.Errorf("Open(%q, %q) deveria falhar", c[0], c[1])
		}
	}
	if f, err := d.Open("v0.5.1", ManifestFile); err != nil {
		t.Errorf("manifesto: %v", err)
	} else {
		f.Close()
	}

	if err := d.Withdraw(); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Current(); v != "" {
		t.Errorf("retirada: %q", v)
	}
}
PATCHD_EOF
echo "criado: internal/release/release_test.go"

mkdir -p internal/version
cat > internal/version/semver.go <<'PATCHD_EOF'
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// CompareSemver compara versões no formato do patchd (SemVer 2.0 com o "v" na frente):
// vMAIOR.MENOR.CORREÇÃO[-pré-lançamento][+build]. O build é ignorado. Pré-lançamento vem
// antes do lançamento (v0.5.0-dev < v0.5.0), e os identificadores do pré-lançamento são
// comparados um a um: numéricos como número, os demais como texto, numérico antes de texto.
func CompareSemver(a, b string) (int, error) {
	pa, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		if c := cmpInt(pa.core[i], pb.core[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(pa.pre) == 0 && len(pb.pre) == 0:
		return 0, nil
	case len(pa.pre) == 0:
		return 1, nil
	case len(pb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		if c := cmpPreID(pa.pre[i], pb.pre[i]); c != 0 {
			return c, nil
		}
	}
	return cmpInt(len(pa.pre), len(pb.pre)), nil
}

type semver struct {
	core [3]int
	pre  []string
}

func parseSemver(v string) (semver, error) {
	var s semver
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return s, fmt.Errorf("versão %q: falta o \"v\" inicial (ex.: v0.5.0)", v)
	}
	rest, _, _ = strings.Cut(rest, "+")
	core, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return s, fmt.Errorf("versão %q: use vMAIOR.MENOR.CORREÇÃO", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return s, fmt.Errorf("versão %q: %q não é um número válido", v, p)
		}
		s.core[i] = n
	}
	if hasPre {
		s.pre = strings.Split(pre, ".")
		for _, id := range s.pre {
			if id == "" {
				return s, fmt.Errorf("versão %q: identificador de pré-lançamento vazio", v)
			}
		}
	}
	return s, nil
}

func cmpPreID(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
PATCHD_EOF
echo "criado: internal/version/semver.go"

mkdir -p internal/version
cat > internal/version/semver_test.go <<'PATCHD_EOF'
package version

import "testing"

func TestCompareSemver(t *testing.T) {
	casos := []struct {
		a, b string
		quer int
	}{
		{"v0.5.0", "v0.5.0", 0},
		{"v0.5.1", "v0.5.0", 1},
		{"v0.10.0", "v0.9.9", 1},     // número, não texto
		{"v0.5.0-dev", "v0.5.0", -1}, // pré-lançamento antes do lançamento
		{"v0.5.1-dev", "v0.5.0", 1},
		{"v0.5.0-rc.2", "v0.5.0-rc.10", -1},
		{"v0.5.0-rc.1", "v0.5.0-rc.a", -1}, // numérico antes de texto
		{"v0.5.0-rc", "v0.5.0-rc.1", -1},
		{"v1.0.0+build.7", "v1.0.0", 0}, // build ignorado
	}
	for _, c := range casos {
		got, err := CompareSemver(c.a, c.b)
		if err != nil || got != c.quer {
			t.Errorf("CompareSemver(%q, %q) = %d, %v; quer %d", c.a, c.b, got, err, c.quer)
		}
	}
	for _, ruim := range []string{"0.5.0", "dev", "v0.5", "v0.5.x", "v01.5.0", "v0.5.0-", "v0.5.0-a..b", "(devel)"} {
		if _, err := CompareSemver(ruim, "v0.5.0"); err == nil {
			t.Errorf("%q deveria ser recusada", ruim)
		}
	}
}
PATCHD_EOF
echo "criado: internal/version/semver_test.go"

python3 - <<'PATCHD_PY'
import pathlib

def patch(path, *pares):
    p = pathlib.Path(path)
    s = p.read_text()
    for velho, novo in pares:
        assert s.count(velho) == 1, f"{path}: âncora não encontrada (ou repetida):\n{velho}"
        s = s.replace(velho, novo)
    p.write_text(s)

# protocolo: a versão do agente na lista de máquinas e a oferta de atualização no check-in.
patch('internal/protocol/api.go',
('''	OSVersion          string     `json:"os_version,omitempty"`
''', '''	OSVersion          string     `json:"os_version,omitempty"`
	AgentVersion       string     `json:"agent_version,omitempty"`        // do último check-in (ou do registro)
'''),
('''// CheckinResponse diz ao agente se ele precisa enviar o inventário.
type CheckinResponse struct {
	InventoryKnown bool      `json:"inventory_known"` // true: o servidor já tem esse inventário; não reenviar
	ServerTime     time.Time `json:"server_time"`
}''', '''// CheckinResponse diz ao agente se ele precisa enviar o inventário e, quando há uma
// versão do agente publicada diferente da dele, qual é (Aula 5.4).
type CheckinResponse struct {
	InventoryKnown bool         `json:"inventory_known"` // true: o servidor já tem esse inventário; não reenviar
	ServerTime     time.Time    `json:"server_time"`
	AgentUpdate    *AgentUpdate `json:"agent_update,omitempty"`
}

// AgentUpdate é a oferta de atualização. É só uma sugestão: o agente baixa o manifesto,
// confere a assinatura com a chave embutida nele e recusa qualquer versão que não seja
// mais nova que a sua.
type AgentUpdate struct {
	Version string `json:"version"`
}'''))

# armazenamento: a versão do agente na lista de máquinas.
patch('internal/store/postgres.go',
('''		SELECT m.id, m.last_seen_at,
''', '''		SELECT m.id, m.last_seen_at, m.agent_version,
'''),
('''		if err := rows.Scan(&s.ID, &s.LastSeenAt, &hostname,''', '''		if err := rows.Scan(&s.ID, &s.LastSeenAt, &s.AgentVersion, &hostname,'''))
patch('internal/store/memory.go',
('''	lastSeenAt         time.Time
}''', '''	lastSeenAt         time.Time
	agentVersion       string
}'''),
('''		s := protocol.MachineSummary{ID: id, LastSeenAt: mm.lastSeenAt}''',
 '''		s := protocol.MachineSummary{ID: id, LastSeenAt: mm.lastSeenAt, AgentVersion: mm.agentVersion}'''),
('''	mm.lastSeenAt = m.now()
	return mm.inventory != nil''', '''	mm.lastSeenAt = m.now()
	mm.agentVersion = agentVersion
	return mm.inventory != nil'''),
('''	m.machine(nm.ID).lastSeenAt = now
''', '''	m.machine(nm.ID).lastSeenAt = now
	m.machine(nm.ID).agentVersion = nm.AgentVersion
'''))
patch('internal/store/checkin_test.go',
('''		t.Errorf("o check-in atualiza o último contato: %v → %v", antes[0].LastSeenAt, depois[0].LastSeenAt)
	}
''', '''		t.Errorf("o check-in atualiza o último contato: %v → %v", antes[0].LastSeenAt, depois[0].LastSeenAt)
	}
	if _, err := st.CheckIn(ctx, m.ID, "v0.5.1", inv.Hash); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.Machines(ctx); l[0].AgentVersion != "v0.5.1" {
		t.Errorf("a lista mostra a versão do último check-in: %q", l[0].AgentVersion)
	}
'''))

# API: opções, a rota de download e a oferta no check-in.
patch('internal/api/api.go',
('''	"github.com/tasantiago/patchd/internal/protocol"
)''', '''	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/release"
)'''),
('''type api struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
}''', '''type api struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time

	releases      release.Dir // vazia: sem atualização automática
	serverVersion string
}'''),
('''func New(st Store, logger *slog.Logger) http.Handler {
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
''', '''func New(st Store, logger *slog.Logger, opts ...Option) http.Handler {
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(a)
	}
'''),
('''	mux.Handle("POST /api/v1/agent/scan", a.machineAuth(a.submitScan))
''', '''	mux.Handle("POST /api/v1/agent/scan", a.machineAuth(a.submitScan))
	mux.Handle("GET /api/v1/agent/releases/{version}/{file}", a.machineAuth(a.releaseFile))
'''))
patch('internal/api/checkin.go',
('''	a.checkClock(r, id, req.SentAt)
	writeJSON(w, http.StatusOK, protocol.CheckinResponse{InventoryKnown: known && req.InventoryHash != "", ServerTime: a.now()})''',
 '''	a.checkClock(r, id, req.SentAt)
	resp := protocol.CheckinResponse{InventoryKnown: known && req.InventoryHash != "", ServerTime: a.now()}
	// A oferta vai quando a versão publicada é outra. Se ela serve, quem decide é o agente:
	// ele confere a assinatura e recusa qualquer versão que não seja mais nova que a sua.
	if v := a.offeredVersion(r); v != "" && v != req.AgentVersion {
		resp.AgentUpdate = &protocol.AgentUpdate{Version: v}
	}
	writeJSON(w, http.StatusOK, resp)'''))

# servidor: a pasta de versões e o subcomando release.
patch('cmd/server/config.go',
('''	"net/url"
''', '''	"net/url"
	"path/filepath"
'''),
('''	LogFormat   string
	ShowVersion bool''', '''	LogFormat   string
	ReleasesDir string // pasta das versões do agente (Aula 5.4); vazia: sem atualização automática
	ShowVersion bool'''),
('''	fs.BoolVar(&cfg.ShowVersion, "version", false, "mostra a versão e sai")
''', '''	fs.StringVar(&cfg.ReleasesDir, "releases-dir", config.String(look, "PATCHD_RELEASES_DIR", ""),
		"pasta das versões assinadas do agente; vazia desliga a atualização automática (env PATCHD_RELEASES_DIR)")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "mostra a versão e sai")
'''),
('''	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		problems = append(problems, err)
	}
''', '''	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		problems = append(problems, err)
	}
	if cfg.ReleasesDir != "" && !filepath.IsAbs(cfg.ReleasesDir) {
		problems = append(problems, fmt.Errorf("-releases-dir/PATCHD_RELEASES_DIR: o caminho precisa ser absoluto (recebido %q)", cfg.ReleasesDir))
	}
'''))
patch('cmd/server/main.go',
('''	"github.com/tasantiago/patchd/internal/logging"
)''', '''	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/release"
)'''),
('''	// Subcomando de administração: "patchd-server token create|list|revoke".
	if len(args) > 0 && args[0] == "token" {
		return runToken(args[1:], look, stdout, stderr)
	}''', '''	// Subcomandos de administração: "patchd-server token create|list|revoke" e
	// "patchd-server release current|publish|withdraw".
	if len(args) > 0 && args[0] == "token" {
		return runToken(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "release" {
		return runRelease(args[1:], look, stdout, stderr, info.Version)
	}'''),
('''	handler := newHandler(api.New(st, logger))''', '''	var apiOpts []api.Option
	if cfg.ReleasesDir != "" {
		apiOpts = append(apiOpts, api.WithReleases(release.Dir(cfg.ReleasesDir), info.Version))
		logReleases(logger, release.Dir(cfg.ReleasesDir), info.Version)
	}
	handler := newHandler(api.New(st, logger, apiOpts...))'''))

# agente: o gancho de atualização no ciclo e no laço do serviço.
patch('internal/agent/agent.go',
('''	Logger    *slog.Logger

	// Injetáveis nos testes.''', '''	Logger    *slog.Logger
	// Update recebe a versão oferecida no check-in (Aula 5.4); nil: o agente não se atualiza.
	Update func(ctx context.Context, version string)

	// Injetáveis nos testes.'''),
('''		"duration", a.now().Sub(start).Round(time.Millisecond).String())
	return outcomeOK''', '''		"duration", a.now().Sub(start).Round(time.Millisecond).String())

	// 4. Atualização do agente, por último: os relatórios deste ciclo já foram entregues.
	if resp.AgentUpdate != nil && a.Update != nil {
		a.Update(ctx, resp.AgentUpdate.Version)
	}
	return outcomeOK'''))
patch('internal/agent/agent_test.go',
('''func novoServidor(t *testing.T) (*servidor, string, string) {
	t.Helper()
	s := &servidor{st: store.NewMemory(), pedidos: map[string]int{}}
	h := api.New(s.st, slog.New(slog.NewTextHandler(io.Discard, nil)))''', '''func novoServidor(t *testing.T, opts ...api.Option) (*servidor, string, string) {
	t.Helper()
	s := &servidor{st: store.NewMemory(), pedidos: map[string]int{}}
	h := api.New(s.st, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)'''))
patch('cmd/agent/loop.go',
('''	AllowContainer bool // PATCHD_ALLOW_CONTAINER=1: só para desenvolvimento
}''', '''	AllowContainer bool // PATCHD_ALLOW_CONTAINER=1: só para desenvolvimento
	// AutoUpdate: só como serviço (Aula 5.4). ServiceArgs são as opções do "service run",
	// repassadas ao install da versão nova para o serviço continuar com as mesmas opções.
	AutoUpdate  bool
	ServiceArgs []string
}'''),
('''	ag := &agent.Agent{
		Client: agent.NewClient(cred.ServerURL, cred.Credential, info.Version),''', '''	client := agent.NewClient(cred.ServerURL, cred.Credential, info.Version)
	ag := &agent.Agent{
		Client: client,'''),
('''	logger.Info("agente iniciado",''', '''	if opts.AutoUpdate {
		ag.Update = newUpdater(logger, client, opts, info.Version)
	}

	logger.Info("agente iniciado",'''))
patch('cmd/agent/service.go',
('''		AllowContainer: allowContainer(look),
	}
	if asService {''', '''		AllowContainer: allowContainer(look),
		AutoUpdate:     asService,
		ServiceArgs:    args,
	}
	if asService {'''))

# build.sh: a chave pública entra nos agentes pelo ambiente, nunca pelo repositório.
patch('scripts/build.sh',
('''LDFLAGS="-s -w -X ${MODULO}/internal/buildinfo.Version=${VERSAO}"
''', '''LDFLAGS="-s -w -X ${MODULO}/internal/buildinfo.Version=${VERSAO}"

# Chave pública de atualização (Aula 5.4): só por variável, nunca no repositório. Sem ela,
# os agentes deste build não se atualizam sozinhos; um build de release exige a chave.
if [ -n "${PATCHD_UPDATE_PUBKEY:-}" ]; then
  if ! [[ "$PATCHD_UPDATE_PUBKEY" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
    echo "ERRO: PATCHD_UPDATE_PUBKEY não parece uma chave do patchd-release keygen (base64 de 32 bytes)." >&2
    exit 1
  fi
  LDFLAGS="${LDFLAGS} -X ${MODULO}/internal/release.PublicKey=${PATCHD_UPDATE_PUBKEY}"
  echo "chave de atualização embutida nos agentes"
elif [[ "$VERSAO" != *-dev ]]; then
  echo "ERRO: build de release ($VERSAO) sem PATCHD_UPDATE_PUBKEY: os agentes não conseguiriam se atualizar." >&2
  exit 1
else
  echo "aviso: sem PATCHD_UPDATE_PUBKEY; os agentes deste build não se atualizam sozinhos" >&2
fi
'''))

p = pathlib.Path('.gitignore')
p.write_text(p.read_text() + '\n# Atualização do agente (Aula 5.4): a chave privada e a pasta de versões nunca entram no git\n*.key\n/releases/\n')
print('ok: 14 arquivos alterados')
PATCHD_PY
gofmt -l . | sed "s/^/gofmt pendente: /"
echo "pronto: rode os testes do Passo 7"
