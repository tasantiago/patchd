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
