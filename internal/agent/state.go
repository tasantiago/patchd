package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Arquivos do agente na pasta de dados (além da credential.json).
const (
	stateFile       = "state.json"        // agenda: quando foi e quando é a próxima busca
	pendingScanFile = "pending-scan.json" // busca feita e ainda não aceita pelo servidor
)

// State é o que precisa sobreviver a um reinício do agente. Sem ele, cada reinício da
// máquina faria uma busca nova (minutos de WUA, com o catálogo offline).
type State struct {
	LastScanAt time.Time `json:"last_scan_at"`
	NextScanAt time.Time `json:"next_scan_at"`
}

func loadState(dir string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil // primeira execução: busca devida
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("%s ilegível (será recriado): %w", stateFile, err)
	}
	return s, nil
}

func saveState(dir string, s State) error {
	return writeJSONAtomic(filepath.Join(dir, stateFile), s)
}

// A busca pendente fica no disco: uma busca leva minutos e não pode se perder porque o
// servidor estava fora ou a máquina reiniciou. Só a mais recente é guardada: ela é a que
// vale para o estado de compliance.
func loadPendingScan(dir string) (*protocol.PatchScanReport, error) {
	data, err := os.ReadFile(filepath.Join(dir, pendingScanFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rep protocol.PatchScanReport
	if err := json.Unmarshal(data, &rep); err != nil {
		_ = os.Remove(filepath.Join(dir, pendingScanFile)) // corrompida: descarta
		return nil, fmt.Errorf("%s ilegível, descartada: %w", pendingScanFile, err)
	}
	return &rep, nil
}

func savePendingScan(dir string, rep protocol.PatchScanReport) error {
	return writeJSONAtomic(filepath.Join(dir, pendingScanFile), rep)
}

func removePendingScan(dir string) error {
	err := os.Remove(filepath.Join(dir, pendingScanFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// writeJSONAtomic grava num temporário da mesma pasta e renomeia: nunca deixa um arquivo
// pela metade. São dados de agenda e de relatório, não segredos: 0600 basta.
func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
