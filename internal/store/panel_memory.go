package store

import (
	"context"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
)

// Usuários e sessões do painel na memória: o mesmo comportamento do PostgreSQL, para os
// testes da API. No modo memória não há como criar usuários pela linha de comando.

// CreatePanelUser cria um usuário local. Nome repetido devolve panel.ErrUserExists.
func (m *Memory) CreatePanelUser(ctx context.Context, u panel.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.panelUsers == nil {
		m.panelUsers = map[string]panel.User{}
	}
	if _, ok := m.panelUsers[u.Name]; ok {
		return panel.ErrUserExists
	}
	u.CreatedAt = m.now()
	m.panelUsers[u.Name] = u
	return nil
}

// PanelUser devolve o usuário local, com o hash da senha.
func (m *Memory) PanelUser(ctx context.Context, name string) (panel.User, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.panelUsers[name]
	return u, ok, nil
}

// PanelUsers lista os usuários locais em ordem de nome, sem os hashes.
func (m *Memory) PanelUsers(ctx context.Context) ([]panel.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := []panel.User{}
	for _, u := range m.panelUsers {
		u.PasswordHash = ""
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// SetPanelPassword troca a senha e encerra as sessões locais do usuário.
func (m *Memory) SetPanelPassword(ctx context.Context, name, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.panelUsers[name]
	if ok {
		u.PasswordHash = hash
		m.panelUsers[name] = u
	}
	m.endLocalSessions(name)
	return ok, nil
}

// DeletePanelUser remove o usuário local e encerra as sessões locais dele.
func (m *Memory) DeletePanelUser(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.panelUsers[name]
	delete(m.panelUsers, name)
	m.endLocalSessions(name)
	return ok, nil
}

// endLocalSessions: chamar com o lock de escrita.
func (m *Memory) endLocalSessions(name string) {
	for k, s := range m.panelSessions {
		if s.Username == name && s.Source == panel.SourceLocal {
			delete(m.panelSessions, k)
		}
	}
}

// CreatePanelSession guarda a sessão pelo hash do segredo.
func (m *Memory) CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.panelSessions == nil {
		m.panelSessions = map[string]panel.Session{}
	}
	m.panelSessions[string(tokenHash)] = s
	return nil
}

// PanelSession devolve a sessão, vencida ou não.
func (m *Memory) PanelSession(ctx context.Context, tokenHash []byte) (panel.Session, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.panelSessions[string(tokenHash)]
	return s, ok, nil
}

// TouchPanelSession registra o uso da sessão.
func (m *Memory) TouchPanelSession(ctx context.Context, tokenHash []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.panelSessions[string(tokenHash)]; ok {
		s.LastSeenAt = at
		m.panelSessions[string(tokenHash)] = s
	}
	return nil
}

// DeletePanelSession encerra a sessão.
func (m *Memory) DeletePanelSession(ctx context.Context, tokenHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.panelSessions, string(tokenHash))
	return nil
}

// DeleteExpiredPanelSessions apaga as sessões vencidas em now.
func (m *Memory) DeleteExpiredPanelSessions(ctx context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.panelSessions {
		if !s.Valid(now) {
			delete(m.panelSessions, k)
			n++
		}
	}
	return n, nil
}
