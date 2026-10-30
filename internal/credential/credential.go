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
	"strings"
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
	// PreviousServerURL (Aula 9.2): o endereço anterior do MESMO servidor, depois de um job
	// server_url. O install com o endereço antigo (script de boot da GPO, por exemplo)
	// continua reconhecendo a máquina como registrada aqui.
	PreviousServerURL string `json:"previous_server_url,omitempty"`
}

// Matches diz se url é o endereço atual ou o anterior do servidor da credencial.
func (c Credential) Matches(url string) bool {
	norm := func(u string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/")) }
	u := norm(url)
	return u != "" && (u == norm(c.ServerURL) || (c.PreviousServerURL != "" && u == norm(c.PreviousServerURL)))
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
	_ = f.Close()
	return os.Remove(name)
}

// Save grava a credencial de forma atômica: arquivo temporário na mesma pasta, acesso
// restrito ANTES de o segredo ser escrito, sincronizado no disco e só então renomeado.
// Uma queda no meio deixa o arquivo antigo (ou nenhum), nunca um arquivo pela metade.
func Save(dataDir string, c Credential) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return SaveSecret(dataDir, FileName, append(data, '\n'))
}

// Secret diz se o arquivo da pasta de dados é um segredo: a credencial e, desde a Aula 21.3,
// a chave da caixa da máquina. Esses ficam com a DACL própria (só SYSTEM e Administradores,
// protegida) também na varredura da pasta (Aula 20.6).
func Secret(name string) bool {
	return name == FileName || name == BoxKeyFile || name == "offline-enrollment.json" || name == "enrollment-ca.crt"
}

// BoxKeyFile: o arquivo da chave privada da caixa (machinebox.FileName; repetido aqui para
// este pacote não depender daquele).
const BoxKeyFile = "chave-cifragem.key"

// SaveSecret grava um segredo na pasta de dados do mesmo jeito que a credencial (Aula 21.3).
func SaveSecret(dataDir, name string, data []byte) error {
	if !Secret(name) {
		return fmt.Errorf("%s não é um arquivo de segredo conhecido", name)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("pasta de dados: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".segredo-*.tmp") // 0600 no Unix
	if err != nil {
		return fmt.Errorf("arquivo temporário: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := restrict(tmpName); err != nil {
		return fmt.Errorf("restringir acesso a %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	dest := filepath.Join(dataDir, name)
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("gravar %s: %w", dest, err)
	}
	ok = true
	return nil
}
