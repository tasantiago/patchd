package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Central do Windows (patchd-app.exe, cmd/app) vem embutida no agente, comprimida, com o
// SHA-256 do executável: o serviço grava exatamente o que saiu do build, sem mudar um byte.
// Antes, o serviço montava a cópia a partir do próprio executável (outro subsistema no
// cabeçalho, outra descrição no recurso de versão), e um antivírus por aprendizado de
// máquina tomava isso por um "dropper" instalando persistência.

// maxUserApp: o executável da Central descomprimido não passa disso (hoje, uns 10 MB).
const maxUserApp = 64 << 20

// errNoEmbeddedApp: o agente foi compilado sem a Central (fora do scripts/build.sh).
var errNoEmbeddedApp = errors.New("este agente foi compilado sem a Central embutida (use o scripts/build.sh)")

// userAppImage descomprime a Central embutida e confere o SHA-256.
func userAppImage(gz []byte, sum string) ([]byte, error) {
	if len(gz) == 0 {
		return nil, errNoEmbeddedApp
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("a Central embutida está ilegível: %w", err)
	}
	img, err := io.ReadAll(io.LimitReader(zr, maxUserApp+1))
	if err != nil {
		return nil, fmt.Errorf("a Central embutida está ilegível: %w", err)
	}
	if len(img) > maxUserApp {
		return nil, errors.New("a Central embutida passa do limite de tamanho")
	}
	got := sha256.Sum256(img)
	if want := strings.TrimSpace(sum); hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("a Central embutida não confere com o SHA-256 do build (%s)", want)
	}
	return img, nil
}

// writeUserApp deixa dest com img. A cópia em uso numa sessão não pode ser apagada, mas pode
// ser renomeada (.old-…): a nova entra no lugar, e a Central aberta percebe e reabre (Aula
// 18.8). Devolve se mudou algo.
func writeUserApp(dest string, img []byte) (bool, error) {
	cleanOldApps(dest)
	if cur, err := os.ReadFile(dest); err == nil && bytes.Equal(cur, img) { // #nosec G304 -- caminho fixo, ao lado do agente
		return false, nil
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, fmt.Sprintf("%s.old-%d", dest, time.Now().UnixNano())); err != nil {
			return false, fmt.Errorf("tirar a cópia antiga do caminho: %w", err)
		}
	}
	tmp := dest + ".novo"
	// #nosec G306 G703 -- a Central para o usuário: executável; dest é fixo (ao lado do agente)
	if err := os.WriteFile(tmp, img, 0o755); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, os.Rename(tmp, dest)
}

// cleanOldApps apaga as cópias antigas que ninguém mais usa; as em uso ficam para a próxima.
func cleanOldApps(dest string) {
	old, _ := filepath.Glob(dest + ".old-*")
	for _, f := range old {
		_ = os.Remove(f)
	}
}
