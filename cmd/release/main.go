// Comando patchd-release: a ferramenta do operador para as versões do agente. Gera o par
// de chaves e assina o manifesto de uma versão. Roda fora do servidor, onde está a chave
// privada; o servidor só recebe a pasta já assinada.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"debug/buildinfo"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
  pubkey -key ARQUIVO                 imprime a pública da chave privada (para refazer o PATCHD_UPDATE_PUBKEY)
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
	case "pubkey":
		return pubkey(args[1:], stdout, stderr)
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
	commit, err := checkBuild(contents, *version)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\nnada foi assinado: rode o scripts/build.sh %s, espere ele terminar e assine de novo\n", err, *version)
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
	fmt.Fprintf(stdout, "versão %s assinada em %s (%d executáveis, commit %s)\n", *version, vdir, len(m.Files), commit)
	return exitOK
}

// checkBuild confere os executáveis antes de assinar (Aula 14.7). Na v0.14.2 o sign rodou
// antes de o build terminar e assinou o dist/ da versão anterior; o agente recusou ("não se
// identificou como v0.14.2"), mas só depois de baixar. Aqui a conferência vem antes:
//   - a versão pedida está dentro de cada executável (o build a injeta com -X; medido: a
//     string aparece uma vez, e a versão anterior não aparece);
//   - cada um é um executável Go com o commit gravado, sem alterações locais, e todos do
//     mesmo commit (o -trimpath não grava o -ldflags, mas grava o vcs.revision).
//
// Devolve o commit (12 dígitos), para quem assina comparar com o git log.
func checkBuild(contents map[string][]byte, version string) (string, error) {
	names := slices.Sorted(maps.Keys(contents))
	commits := map[string][]string{}
	for _, name := range names {
		data := contents[name]
		if !bytes.Contains(data, []byte(version)) {
			return "", fmt.Errorf("%s não é da versão %s (o build ainda não terminou, ou o dist/ é de outra versão)", name, version)
		}
		bi, err := buildinfo.Read(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("%s não é um executável Go com informação de build: %v", name, err)
		}
		rev, modified := "", false
		for _, st := range bi.Settings {
			switch st.Key {
			case "vcs.revision":
				rev = st.Value
			case "vcs.modified":
				modified = st.Value == "true"
			}
		}
		if modified {
			return "", fmt.Errorf("%s foi compilado com alterações não commitadas", name)
		}
		commits[rev] = append(commits[rev], name)
	}
	if len(commits) > 1 {
		var parts []string
		for _, rev := range slices.Sorted(maps.Keys(commits)) {
			parts = append(parts, fmt.Sprintf("%s: %s", shortRev(rev), strings.Join(commits[rev], ", ")))
		}
		return "", fmt.Errorf("os executáveis vêm de commits diferentes (%s)", strings.Join(parts, "; "))
	}
	for rev := range commits {
		return shortRev(rev), nil
	}
	return "", nil
}

func shortRev(rev string) string {
	switch {
	case rev == "":
		return "sem commit gravado"
	case len(rev) > 12:
		return rev[:12]
	}
	return rev
}

// pubkey imprime a chave pública da chave privada (Aula 9.1b): o PATCHD_UPDATE_PUBKEY se
// refaz a partir da privada, sem gerar outro par (um par novo obrigaria a reinstalar a
// frota com um executável que confie nele). Só a pública sai; a privada nunca é impressa.
func pubkey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyPath := fs.String("key", "", "arquivo da chave privada")
	if err := fs.Parse(args); err != nil || *keyPath == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return exitConfig
	}
	priv, err := loadPrivateKey(*keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-release: %v\n", err)
		return exitConfig
	}
	fmt.Fprintln(stdout, release.EncodePublicKey(priv.Public().(ed25519.PublicKey)))
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
