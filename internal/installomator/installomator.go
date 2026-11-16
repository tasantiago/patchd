// Package installomator leva o Installomator (https://github.com/Installomator/Installomator,
// licença Apache 2.0) aos Macs: o script que baixa um programa do fabricante, confere o Team ID
// da assinatura da Apple e instala, um rótulo por programa (googlechromepkg, firefoxpkg).
//
// O patchd não roda o script no servidor nem confia na rede da máquina para buscá-lo: o
// servidor baixa a versão fixada aqui, só guarda se o SHA-256 for o fixado, e o entrega aos
// agentes como entrega os instaladores do Windows (pacotes/<sha256>.sh). O agente confere o
// SHA-256 e o tamanho do job assinado antes de rodar.
//
// Trocar de versão é trocar Version e SHA256 aqui (depois de ler o que mudou), ou, sem
// recompilar, PATCHD_INSTALLOMATOR_VERSION e PATCHD_INSTALLOMATOR_SHA256 juntos.
package installomator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A versão fixada: a tag v10.10 (12/11/2026), Installomator.sh de 677.099 bytes.
const (
	Version = "10.10"
	SHA256  = "3ed5d385cd8d4a0d8f61c58ee11d89322f0298ed4700863a62cb13857ff175db"
	// BaseURL: o arquivo da tag no GitHub (raw); um espelho interno pode substituir.
	BaseURL = "https://raw.githubusercontent.com/Installomator/Installomator"
	// MaxSize: o script tem ~700 KB; mais que isso não é ele.
	MaxSize = 4 << 20
)

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	version   = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){0,2}$`)
)

// Script é a versão do Installomator que o servidor leva.
type Script struct {
	Version string
	SHA256  string
}

// FileName: o nome no cache de pacotes (a mesma pasta dos instaladores do Windows).
func FileName(sha string) string { return sha + ".sh" }

// URL: o endereço do script da versão.
func (s Script) URL(base string) string {
	return strings.TrimSuffix(base, "/") + "/v" + s.Version + "/Installomator.sh"
}

// Pinned devolve a versão fixada, ou a das variáveis (as duas juntas).
func Pinned(look func(string) (string, bool)) (Script, error) {
	v, okV := look("PATCHD_INSTALLOMATOR_VERSION")
	h, okH := look("PATCHD_INSTALLOMATOR_SHA256")
	switch {
	case !okV && !okH:
		return Script{Version: Version, SHA256: SHA256}, nil
	case !okV || !okH:
		return Script{}, errors.New("PATCHD_INSTALLOMATOR_VERSION e PATCHD_INSTALLOMATOR_SHA256 vão juntas")
	case !version.MatchString(v):
		return Script{}, fmt.Errorf("PATCHD_INSTALLOMATOR_VERSION %q: o número da versão (10.10)", v)
	case !sha256Hex.MatchString(strings.ToLower(h)):
		return Script{}, errors.New("PATCHD_INSTALLOMATOR_SHA256: 64 dígitos hexadecimais")
	}
	return Script{Version: v, SHA256: strings.ToLower(h)}, nil
}

// Check confere o script no cache (pacotes): existe e tem o SHA-256. Devolve o tamanho.
func Check(dir string, s Script) (int64, error) {
	f, err := os.Open(filepath.Join(dir, FileName(s.SHA256))) // #nosec G304 -- nome = SHA-256 conferido
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxSize+1))
	switch {
	case err != nil:
		return 0, err
	case n > MaxSize:
		return n, fmt.Errorf("o arquivo passa de %d bytes", MaxSize)
	case hex.EncodeToString(h.Sum(nil)) != s.SHA256:
		return n, errors.New("o conteúdo não confere com o SHA-256")
	}
	return n, nil
}

// Fetch garante o script no cache: confere o que já está lá, ou baixa de base e só guarda se o
// SHA-256 for o da versão. cached diz que já estava.
func Fetch(ctx context.Context, client *http.Client, userAgent, base, dir string, s Script) (size int64, cached bool, err error) {
	if n, err := Check(dir, s); err == nil {
		return n, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(filepath.Join(dir, FileName(s.SHA256))) // não confere: baixa de novo
	}
	u := s.URL(base)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return 0, false, fmt.Errorf("endereço %q: só https", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, false, err
	}
	tmp, err := os.CreateTemp(dir, ".installomator-*")
	if err != nil {
		return 0, false, err
	}
	defer os.Remove(tmp.Name()) // sem efeito depois do Rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, MaxSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return 0, false, err
	case n > MaxSize:
		return 0, false, fmt.Errorf("%s: mais de %d bytes", u, MaxSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != s.SHA256 {
		return 0, false, fmt.Errorf("o Installomator %s baixado tem SHA-256 %s…, não o fixado %s…: nada foi guardado", s.Version, got[:12], s.SHA256[:12])
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, FileName(s.SHA256))); err != nil {
		return 0, false, err
	}
	return n, false, nil
}
