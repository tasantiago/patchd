// Package credential guarda, no disco do agente, a identidade emitida pelo servidor no
// enrollment: o ID da máquina e a credencial. É o segredo mais sensível da máquina: quem
// o lê envia relatórios em nome dela.
package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileName é o nome do arquivo dentro da pasta de dados do agente.
const FileName = "credential.json"

// Credential é o conteúdo do arquivo.
type Credential struct {
	MachineID  string    `json:"machine_id"`
	Credential string    `json:"credential"`
	ServerURL  string    `json:"server_url"` // a credencial só vale no servidor que a emitiu
	EnrolledAt time.Time `json:"enrolled_at"`
}

// ErrNotEnrolled: o arquivo não existe (a máquina ainda não se registrou).
var ErrNotEnrolled = errors.New("máquina não registrada")

// Path devolve o caminho do arquivo na pasta de dados.
func Path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// Load lê a credencial. Sem arquivo: ErrNotEnrolled.
func Load(dataDir string) (Credential, error) {
	var c Credential
	data, err := os.ReadFile(Path(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return c, ErrNotEnrolled
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s ilegível: %w", Path(dataDir), err)
	}
	if c.MachineID == "" || c.Credential == "" {
		return c, fmt.Errorf("%s incompleto", Path(dataDir))
	}
	return c, nil
}

// CheckWritable confere, ANTES do registro, que a credencial poderá ser gravada. Sem isso,
// uma pasta sem permissão faria o servidor registrar a máquina e consumir um uso do
// token, e a credencial emitida se perderia.
func CheckWritable(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("pasta de dados: %w", err)
	}
	f, err := os.CreateTemp(dataDir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("sem permissão de escrita em %s: %w", dataDir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Save grava a credencial de forma atômica: arquivo temporário na mesma pasta, acesso
// restrito ANTES de o segredo ser escrito, sincronizado no disco e só então renomeado.
// Uma queda no meio deixa o arquivo antigo (ou nenhum), nunca um arquivo pela metade.
func Save(dataDir string, c Credential) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("pasta de dados: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dataDir, ".credential-*.tmp") // 0600 no Unix
	if err != nil {
		return fmt.Errorf("arquivo temporário: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()

	if err := restrict(name); err != nil {
		return fmt.Errorf("restringir acesso a %s: %w", name, err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, Path(dataDir)); err != nil {
		return fmt.Errorf("gravar %s: %w", Path(dataDir), err)
	}
	ok = true
	return nil
}
