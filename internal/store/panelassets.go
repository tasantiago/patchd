package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// Arquivos do painel guardados no banco (Aula 19.1): por enquanto, só o logotipo.

// MaxPanelAsset: o maior arquivo aceito (o mesmo limite da migração 0037).
const MaxPanelAsset = 256 << 10

// PanelAsset é um arquivo do painel. SHA256 sai do conteúdo na gravação.
type PanelAsset struct {
	Name        string
	ContentType string
	Data        []byte
	SHA256      string
	UpdatedAt   time.Time
	UpdatedBy   string
}

var assetName = regexp.MustCompile(`^[a-z]{1,20}$`)

// checkAsset confere o que a migração também confere, para a memória recusar igual ao banco.
func checkAsset(a *PanelAsset) error {
	switch {
	case !assetName.MatchString(a.Name):
		return fmt.Errorf("nome de arquivo do painel inválido: %q", a.Name)
	case a.ContentType != "image/png" && a.ContentType != "image/jpeg":
		return fmt.Errorf("tipo não aceito: %q", a.ContentType)
	case len(a.Data) == 0 || len(a.Data) > MaxPanelAsset:
		return fmt.Errorf("tamanho fora do limite: %d bytes (máximo %d)", len(a.Data), MaxPanelAsset)
	}
	sum := sha256.Sum256(a.Data)
	a.SHA256 = hex.EncodeToString(sum[:])
	return nil
}

// PanelAsset devolve o arquivo; found=false quando não há.
func (p *Postgres) PanelAsset(ctx context.Context, name string) (PanelAsset, bool, error) {
	a := PanelAsset{Name: name}
	err := p.pool.QueryRow(ctx, `SELECT content_type, data, sha256, updated_at, updated_by FROM panel_assets WHERE name = $1`, name).
		Scan(&a.ContentType, &a.Data, &a.SHA256, &a.UpdatedAt, &a.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return PanelAsset{}, false, nil
	}
	if err != nil {
		return PanelAsset{}, false, err
	}
	return a, true, nil
}

// SetPanelAsset grava (ou troca) o arquivo.
func (p *Postgres) SetPanelAsset(ctx context.Context, a PanelAsset) error {
	if err := checkAsset(&a); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO panel_assets (name, content_type, data, sha256, updated_at, updated_by)
VALUES ($1, $2, $3, $4, now(), $5)
ON CONFLICT (name) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data, sha256 = EXCLUDED.sha256,
  updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`, a.Name, a.ContentType, a.Data, a.SHA256, a.UpdatedBy)
	return err
}

// DeletePanelAsset apaga o arquivo; false quando não havia.
func (p *Postgres) DeletePanelAsset(ctx context.Context, name string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM panel_assets WHERE name = $1`, name)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// PanelAsset: na memória.
func (m *Memory) PanelAsset(ctx context.Context, name string) (PanelAsset, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assets[name]
	if ok {
		a.Data = append([]byte(nil), a.Data...)
	}
	return a, ok, nil
}

// SetPanelAsset: na memória.
func (m *Memory) SetPanelAsset(ctx context.Context, a PanelAsset) error {
	if err := checkAsset(&a); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assets == nil {
		m.assets = map[string]PanelAsset{}
	}
	a.Data = append([]byte(nil), a.Data...)
	a.UpdatedAt = m.now().UTC()
	m.assets[a.Name] = a
	return nil
}

// DeletePanelAsset: na memória.
func (m *Memory) DeletePanelAsset(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.assets[name]
	delete(m.assets, name)
	return ok, nil
}
