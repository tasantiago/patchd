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
