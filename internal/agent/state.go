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
	// LastJobID é o maior job aceito (Aula 8.1): um job com ID menor ou igual é recusado,
	// e uma resposta antiga copiada não faz o mesmo job rodar duas vezes.
	LastJobID int64 `json:"last_job_id,omitempty"`
	// PendingJobResults são resultados ainda não aceitos pelo servidor (ele estava fora).
	PendingJobResults []PendingJobResult `json:"pending_job_results,omitempty"`
	// PendingReboot é o reinício agendado por um job (Aula 8.6): depois que a máquina
	// volta, o agente confere a hora do boot, faz a busca e manda o resultado.
	PendingReboot *PendingReboot `json:"pending_reboot,omitempty"`
	// PendingServerMove é a troca de endereço aceita (Aula 9.2), feita no fim de um ciclo
	// entregue: se o agente cair antes, ela fica para o próximo.
	PendingServerMove *PendingServerMove `json:"pending_server_move,omitempty"`
	// InventoryFirst (Aula 14.4): houve instalação de terceiros e o inventário de depois
	// ainda não chegou ao servidor. Os resultados esperam: o servidor confere o job pelo
	// inventário, e um resultado antes dele viraria "falhou".
	InventoryFirst bool `json:"inventory_first,omitempty"`
}

// PendingServerMove guarda o job server_url até a troca ser tentada.
type PendingServerMove struct {
	JobID int64  `json:"job_id"`
	URL   string `json:"url"`
}

// PendingReboot guarda o job de reinício até a máquina voltar.
type PendingReboot struct {
	JobID      int64     `json:"job_id"`
	BootBefore time.Time `json:"boot_before"` // último boot antes do agendamento
	At         time.Time `json:"at"`          // quando o reinício foi marcado
}

// PendingJobResult é o resultado de um job à espera de envio.
type PendingJobResult struct {
	ID     int64              `json:"id"`
	Result protocol.JobResult `json:"result"`
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
