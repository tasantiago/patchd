package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Memory guarda o último relatório de cada máquina na memória do processo. É o
// armazenamento da Aula 4.1: tudo se perde ao reiniciar. O PostgreSQL entra na 4.2,
// cumprindo a mesma interface, sem mudar a API.
type Memory struct {
	mu       sync.RWMutex
	machines map[string]*memMachine
	now      func() time.Time
}

type memMachine struct {
	inventory          *protocol.InventoryReport
	inventoryChangedAt time.Time
	scan               *protocol.PatchScanReport
	scanReceivedAt     time.Time
	lastSeenAt         time.Time
}

// NewMemory cria um armazenamento vazio.
func NewMemory() *Memory {
	return &Memory{
		machines: map[string]*memMachine{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// machine devolve o registro da máquina, criando-o se preciso. Chamar com o lock de escrita.
func (m *Memory) machine(id string) *memMachine {
	mm := m.machines[id]
	if mm == nil {
		mm = &memMachine{}
		m.machines[id] = mm
	}
	return mm
}

// SaveInventory grava o relatório se o hash for diferente do último da máquina.
// Devolve true se gravou, false se nada mudou. Nos dois casos, a máquina foi vista agora.
func (m *Memory) SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mm := m.machine(id)
	now := m.now()
	mm.lastSeenAt = now
	if mm.inventory != nil && mm.inventory.Hash == rep.Hash {
		return false, nil
	}
	mm.inventory = &rep
	mm.inventoryChangedAt = now
	return true, nil
}

// LatestInventory devolve o último inventário guardado da máquina.
func (m *Memory) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil || mm.inventory == nil {
		return protocol.InventoryReport{}, false, nil
	}
	return *mm.inventory, true, nil
}

// SaveScan grava a última busca de atualizações da máquina.
func (m *Memory) SaveScan(ctx context.Context, id string, rep protocol.PatchScanReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machine(id)
	now := m.now()
	mm.lastSeenAt = now
	mm.scan = &rep
	mm.scanReceivedAt = now
	return nil
}

// LatestScan devolve a última busca guardada da máquina.
func (m *Memory) LatestScan(ctx context.Context, id string) (protocol.PatchScanReport, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[id]
	if mm == nil || mm.scan == nil {
		return protocol.PatchScanReport{}, false, nil
	}
	return *mm.scan, true, nil
}

// Machines resume as máquinas conhecidas, em ordem de ID.
func (m *Memory) Machines(ctx context.Context) ([]protocol.MachineSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]protocol.MachineSummary, 0, len(m.machines))
	for id, mm := range m.machines {
		s := protocol.MachineSummary{ID: id, LastSeenAt: mm.lastSeenAt}
		if mm.inventory != nil {
			s.Hostname = mm.inventory.OS.Hostname
			s.OSName = mm.inventory.OS.Name
			s.OSVersion = mm.inventory.OS.Version
			s.InventoryHash = mm.inventory.Hash
			t := mm.inventoryChangedAt
			s.InventoryChangedAt = &t
		}
		if mm.scan != nil {
			t := mm.scanReceivedAt
			s.ScanReceivedAt = &t
			if mm.scan.Reboot != nil {
				s.RebootPending = mm.scan.Reboot.Pending
			}
		}
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}
