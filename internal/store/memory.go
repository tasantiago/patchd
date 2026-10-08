package store

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Memory guarda tudo na memória do processo. Serve para desenvolvimento e para os
// testes da API; em produção, o armazenamento é o PostgreSQL (Aula 4.2).
type Memory struct {
	mu          sync.RWMutex
	machines    map[string]*memMachine
	tokens      []*memToken
	credentials map[string]string // hash da credencial (como texto) → ID da máquina
	keys        []memKeys         // chaves de identidade, na ordem de registro
	links       []protocol.IdentityLink
	now         func() time.Time

	panelUsers    map[string]panel.User    // usuários locais do painel, pelo nome
	panelSessions map[string]panel.Session // sessões do painel, pelo hash do segredo (como texto)
}

type memMachine struct {
	inventory          *protocol.InventoryReport
	inventoryChangedAt time.Time
	scan               *protocol.PatchScanReport
	scanReceivedAt     time.Time
	lastSeenAt         time.Time
	agentVersion       string
	retiredAt          time.Time // zero: na frota
	retiredReason      string
}

type memKeys struct {
	id   string
	keys identity.Keys
}

type memToken struct {
	identity.EnrollmentToken
	hash []byte
}

// NewMemory cria um armazenamento vazio.
func NewMemory() *Memory {
	return &Memory{
		machines:    map[string]*memMachine{},
		credentials: map[string]string{},
		now:         func() time.Time { return time.Now().UTC() },
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

// Machines resume as máquinas da frota (sem as aposentadas), em ordem de ID.
func (m *Memory) Machines(ctx context.Context) ([]protocol.MachineSummary, error) {
	list := m.summaries(false)
	out := make([]protocol.MachineSummary, len(list))
	for i, r := range list {
		out[i] = r.MachineSummary
	}
	return out, nil
}

// summaries monta o resumo das máquinas ativas ou das aposentadas.
func (m *Memory) summaries(retired bool) []RetiredMachine {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]RetiredMachine, 0, len(m.machines))
	for id, mm := range m.machines {
		if mm.retiredAt.IsZero() == retired {
			continue
		}
		s := protocol.MachineSummary{ID: id, LastSeenAt: mm.lastSeenAt, AgentVersion: mm.agentVersion}
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
		list = append(list, RetiredMachine{MachineSummary: s, RetiredAt: mm.retiredAt, Reason: mm.retiredReason})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// CheckIn registra o contato periódico e diz se o inventário atual tem o hash informado.
func (m *Memory) CheckIn(ctx context.Context, id, agentVersion, inventoryHash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mm := m.machines[id]
	if mm == nil {
		return false, fmt.Errorf("máquina %s desconhecida", id)
	}
	mm.lastSeenAt = m.now()
	mm.agentVersion = agentVersion
	return mm.inventory != nil && mm.inventory.Hash == inventoryHash, nil
}

// CreateEnrollmentToken guarda um token novo (só o hash) e devolve o ID dele.
func (m *Memory) CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := &memToken{hash: hash, EnrollmentToken: identity.EnrollmentToken{
		ID: int64(len(m.tokens) + 1), Note: note, CreatedAt: m.now(), ExpiresAt: expiresAt.UTC(), MaxUses: maxUses,
	}}
	m.tokens = append(m.tokens, t)
	return t.ID, nil
}

// EnrollmentTokens lista os tokens, do mais novo para o mais antigo.
func (m *Memory) EnrollmentTokens(ctx context.Context) ([]identity.EnrollmentToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]identity.EnrollmentToken, 0, len(m.tokens))
	for i := len(m.tokens) - 1; i >= 0; i-- {
		list = append(list, m.tokens[i].EnrollmentToken)
	}
	return list, nil
}

// RevokeEnrollmentToken revoga um token. Devolve false se o ID não existe.
func (m *Memory) RevokeEnrollmentToken(ctx context.Context, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.ID == id {
			if t.RevokedAt == nil {
				now := m.now()
				t.RevokedAt = &now
			}
			return true, nil
		}
	}
	return false, nil
}

// Enroll consome um uso do token, registra a máquina e cria os alertas de identidade.
func (m *Memory) Enroll(ctx context.Context, tokenHash []byte, nm identity.NewMachine) ([]protocol.IdentityLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tok *memToken
	for _, t := range m.tokens {
		if bytes.Equal(t.hash, tokenHash) {
			tok = t
		}
	}
	if tok == nil {
		return nil, fmt.Errorf("%w: token desconhecido", identity.ErrEnrollmentRejected)
	}
	if st := tok.Status(m.now()); st != "válido" {
		return nil, fmt.Errorf("%w: token %d %s", identity.ErrEnrollmentRejected, tok.ID, st)
	}
	if _, exists := m.machines[nm.ID]; exists {
		return nil, fmt.Errorf("ID de máquina repetido: %s", nm.ID)
	}
	tok.Uses++
	now := m.now()
	m.machine(nm.ID).lastSeenAt = now
	m.machine(nm.ID).agentVersion = nm.AgentVersion
	m.credentials[string(nm.CredentialHash)] = nm.ID

	links := []protocol.IdentityLink{}
	for _, c := range m.keys {
		if rel, ok := identity.Relation(nm.Keys, c.keys); ok {
			l := protocol.IdentityLink{ID: int64(len(m.links) + 1), MachineID: nm.ID, RelatedMachineID: c.id, Relation: rel, CreatedAt: now}
			m.links = append(m.links, l)
			links = append(links, l)
		}
	}
	m.keys = append(m.keys, memKeys{id: nm.ID, keys: nm.Keys})
	return links, nil
}

// IdentityLinks lista os alertas de identidade, do mais antigo para o mais novo.
func (m *Memory) IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]protocol.IdentityLink{}, m.links...), nil
}

// MachineByCredential devolve a máquina dona da credencial.
func (m *Memory) MachineByCredential(ctx context.Context, credentialHash []byte) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.credentials[string(credentialHash)]
	if mm := m.machines[id]; ok && mm != nil && !mm.retiredAt.IsZero() {
		return "", false, nil // aposentada: a credencial não vale (Aula 7.5)
	}
	return id, ok, nil
}
