// Package tpupload guarda o instalador próprio de um programa de terceiros (docs/operacao.md,
// "Instaladores próprios e atalho"): o .msi ou .exe que o fabricante distribui fora do winget
// (o IRPF, um sistema interno), enviado pelo painel ou pelo package upload.
//
// O arquivo entra no mesmo cache dos instaladores do winget (pkgcache: o SHA-256 no nome) e
// vira um registro de instalador como os outros, com a origem "upload:". Daí em diante o
// caminho é o de sempre: a aprovação da versão, a campanha, a Central de software e a
// padronização usam o instalador sem saber de onde ele veio; o agente confere o SHA-256 e a
// assinatura antes de executar. A versão de referência do programa é a do instalador mais novo
// enviado.
package tpupload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
	"github.com/tasantiago/patchd/internal/winget"
)

// MaxSize: o maior instalador (o mesmo teto do job e do cache).
const MaxSize = pkgcache.DefaultMaxSize

// Archs: as arquiteturas que o envio aceita (neutral: roda em qualquer uma).
var Archs = []string{"x64", "x86", "arm64", "neutral"}

// Store é o que o envio grava.
type Store interface {
	SaveThirdPartyPackage(ctx context.Context, p store.ThirdPartyPackage) (bool, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	SaveThirdPartyVersion(ctx context.Context, app, osID, version, source string) error
}

// Input é o que acompanha o arquivo.
type Input struct {
	Version  string // a versão do instalador, como aparece em Programas e Recursos depois de instalar
	Arch     string // Archs
	Args     string // os argumentos da instalação silenciosa; vazio: o padrão do tipo (msi, nullsoft, inno)
	FileName string // o nome do arquivo enviado, só para mostrar
}

// ErrNotUpload: a regra não é de instalador próprio.
var ErrNotUpload = errors.New("o programa não é de instalador próprio: o instalador dele vem do winget (Baixar)")

// Problem é um envio recusado pelo conteúdo (o painel mostra a mensagem).
type Problem struct{ Msg string }

func (p *Problem) Error() string { return p.Msg }

func problem(format string, a ...any) error { return &Problem{Msg: fmt.Sprintf(format, a...)} }

var safeName = regexp.MustCompile(`[^A-Za-z0-9 ._()+-]`)

// displayName: o nome do arquivo para a url "upload:" (sem pasta, sem caracteres estranhos).
func displayName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = safeName.ReplaceAllString(name, "_")
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	if name == "" || name == "." {
		name = "instalador"
	}
	return name
}

// Args: os argumentos que valem para o tipo (os digitados ou o padrão), conferidos como no job.
func Args(installType, line string) (string, error) {
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		line = winget.SilentDefault(installType)
	}
	if line == "" {
		return "", problem("o tipo %s não tem argumentos padrão: diga os da instalação silenciosa (o fabricante documenta; ex.: /S, /quiet, -q)", installType)
	}
	if _, err := jobs.SplitArgs(installType, line); err != nil {
		return "", problem("argumentos: %v", err)
	}
	return line, nil
}

// check confere a versão, a arquitetura e os argumentos antes de receber o arquivo.
func check(rule thirdparty.App, in Input) (string, error) {
	if !rule.Uploaded() {
		return "", ErrNotUpload
	}
	if !jobs.ValidThirdPartyVersion(in.Version) {
		return "", problem("versão %q: comece por um número (ex.: 1.2, 2026.1.0)", in.Version)
	}
	if _, ok := thirdparty.Normalize(in.Version); !ok {
		return "", problem("versão %q não se compara com a instalada: use números com pontos (ex.: 1.2)", in.Version)
	}
	if !slices.Contains(Archs, in.Arch) {
		return "", problem("arquitetura %q: use %s", in.Arch, strings.Join(Archs, ", "))
	}
	return Args(rule.Install.Type, in.Args)
}

// Check confere o que acompanha o arquivo, antes de recebê-lo (o painel não começa a gravar
// um arquivo de centenas de MB para recusar a versão no fim).
func Check(rule thirdparty.App, in Input) error {
	_, err := check(rule, in)
	return err
}

// magic: o começo do arquivo de cada tipo (o MSI é um arquivo composto do OLE; o exe, um PE).
var (
	msiMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	exeMagic = []byte("MZ")
)

// Save recebe o arquivo (até MaxSize), confere o tipo pelo conteúdo, guarda no cache de
// pacotes (dir) com o SHA-256 no nome e registra o instalador. A versão de referência passa a
// ser a mais nova dos instaladores enviados. Devolve o registro.
func Save(ctx context.Context, st Store, dir string, rule thirdparty.App, in Input, r io.Reader) (store.ThirdPartyPackage, error) {
	args, err := check(rule, in)
	if err != nil {
		return store.ThirdPartyPackage{}, err
	}
	if dir == "" {
		return store.ThirdPartyPackage{}, problem("o servidor não guarda instaladores (PATCHD_CONTENT_DIR)")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return store.ThirdPartyPackage{}, err
	}
	tmp, err := os.CreateTemp(dir, ".enviando-*")
	if err != nil {
		return store.ThirdPartyPackage{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	var head bytes.Buffer
	n, err := io.Copy(io.MultiWriter(tmp, h, &limitBuffer{b: &head, max: len(msiMagic)}), io.LimitReader(r, MaxSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return store.ThirdPartyPackage{}, err
	case n == 0:
		return store.ThirdPartyPackage{}, problem("o arquivo veio vazio")
	case n > MaxSize:
		return store.ThirdPartyPackage{}, problem("o arquivo passa de %s", pkgcache.Bytes(MaxSize))
	}
	msi := jobs.MSIInstaller(rule.Install.Type)
	switch {
	case msi && !bytes.HasPrefix(head.Bytes(), msiMagic):
		return store.ThirdPartyPackage{}, problem("o programa instala por MSI, e o arquivo não é um .msi")
	case !msi && !bytes.HasPrefix(head.Bytes(), exeMagic):
		return store.ThirdPartyPackage{}, problem("o programa instala por %s, e o arquivo não é um executável do Windows (.exe)", rule.Install.Type)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	pkgs, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		return store.ThirdPartyPackage{}, err
	}
	for _, p := range pkgs {
		if p.AppID == rule.ID && p.Version == in.Version && p.Architecture == in.Arch && p.Type == rule.Install.Type && p.SHA256 != sum {
			return store.ThirdPartyPackage{}, problem("a versão %s (%s) já foi enviada com outro arquivo: exclua a versão antes, ou use outro número",
				in.Version, in.Arch)
		}
	}
	// O conteúdo antes do registro: um erro no banco pode deixar um arquivo sem registro,
	// nunca um registro sem o arquivo.
	final := filepath.Join(dir, pkgcache.FileName(sum, msi))
	if err := os.Rename(tmp.Name(), final); err != nil {
		return store.ThirdPartyPackage{}, err
	}
	pk := store.ThirdPartyPackage{AppID: rule.ID, Version: in.Version, Architecture: in.Arch, Type: rule.Install.Type,
		Scope: map[bool]string{true: "user", false: "machine"}[rule.PerUser()], URL: store.UploadPrefix + displayName(in.FileName), SHA256: sum, Size: n, Arguments: args}
	if _, err := st.SaveThirdPartyPackage(ctx, pk); err != nil {
		return store.ThirdPartyPackage{}, err // a corrida com outro envio da mesma versão: PackageChangedError
	}
	if err := SaveReference(ctx, st, rule); err != nil {
		return pk, err
	}
	return pk, nil
}

// SaveReference grava a versão de referência do programa: a mais nova entre os instaladores
// guardados dele. Sem nenhum, fica a que estava.
func SaveReference(ctx context.Context, st Store, rule thirdparty.App) error {
	pkgs, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		return err
	}
	best := ""
	for _, p := range pkgs {
		if p.AppID != rule.ID {
			continue
		}
		if best == "" {
			best = p.Version
			continue
		}
		if c, err := version.CompareDotted(norm(p.Version), norm(best)); err == nil && c > 0 {
			best = p.Version
		}
	}
	if best == "" {
		return nil
	}
	return st.SaveThirdPartyVersion(ctx, rule.ID, rule.OS, norm(best), rule.Source.String())
}

func norm(v string) string {
	n, _ := thirdparty.Normalize(v)
	return n
}

// limitBuffer guarda só os primeiros max bytes (o começo do arquivo, para conferir o tipo).
type limitBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
