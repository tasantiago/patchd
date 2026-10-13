package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Instaladores de terceiros guardados pelo servidor (Aula 14.2).

// ThirdPartyPackage é um instalador baixado e conferido.
type ThirdPartyPackage struct {
	AppID, WingetID, Version  string
	Architecture, Type, Scope string
	URL, SHA256, Arguments    string
	Size                      int64
	ReturnCodes               map[int]string // código de saída → resposta do winget
	FetchedAt                 time.Time
}

// MSI: o msiexec executa (msi e wix); o arquivo do cache termina em .msi.
func (p ThirdPartyPackage) MSI() bool { return p.Type == "msi" || p.Type == "wix" }

// PackageChangedError: a mesma versão já está guardada com outro SHA-256.
type PackageChangedError struct {
	Stored ThirdPartyPackage
	SHA256 string
}

func (e *PackageChangedError) Error() string {
	return fmt.Sprintf("%s %s (%s, %s) já está guardado com o SHA-256 %s…; o manifesto agora diz %s…: a mesma versão com outro binário não é aceita",
		e.Stored.AppID, e.Stored.Version, e.Stored.Type, e.Stored.Architecture, e.Stored.SHA256[:12], e.SHA256[:12])
}

func packageKey(p ThirdPartyPackage) string {
	return p.AppID + "\x00" + p.Version + "\x00" + p.Architecture + "\x00" + p.Type
}

// SaveThirdPartyPackage registra o instalador. created false: já estava, com o mesmo
// SHA-256 (só a data é atualizada). A mesma chave com outro SHA-256 devolve
// *PackageChangedError e não muda nada.
func (p *Postgres) SaveThirdPartyPackage(ctx context.Context, pk ThirdPartyPackage) (bool, error) {
	codes, err := json.Marshal(pk.ReturnCodes)
	if err != nil {
		return false, err
	}
	if pk.ReturnCodes == nil {
		codes = []byte("{}")
	}
	var inserted bool
	err = p.pool.QueryRow(ctx, `
		INSERT INTO thirdparty_packages (app_id, winget_id, version, architecture, installer_type, scope, url, sha256, size,
			arguments, return_codes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb)
		ON CONFLICT (app_id, version, architecture, installer_type) DO UPDATE SET fetched_at = now()
			WHERE thirdparty_packages.sha256 = EXCLUDED.sha256
		RETURNING xmax = 0`,
		pk.AppID, pk.WingetID, pk.Version, pk.Architecture, pk.Type, pk.Scope, pk.URL, pk.SHA256, pk.Size,
		pk.Arguments, codes).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		// Conflito com outro SHA-256: o WHERE do DO UPDATE não deixou a linha mudar.
		var stored ThirdPartyPackage
		err := p.pool.QueryRow(ctx, `
			SELECT app_id, version, architecture, installer_type, sha256 FROM thirdparty_packages
			WHERE app_id = $1 AND version = $2 AND architecture = $3 AND installer_type = $4`,
			pk.AppID, pk.Version, pk.Architecture, pk.Type).Scan(&stored.AppID, &stored.Version, &stored.Architecture, &stored.Type, &stored.SHA256)
		if err != nil {
			return false, err
		}
		return false, &PackageChangedError{Stored: stored, SHA256: pk.SHA256}
	}
	return inserted, err
}

// ThirdPartyPackages lista os instaladores guardados, por programa e do mais recente.
func (p *Postgres) ThirdPartyPackages(ctx context.Context) ([]ThirdPartyPackage, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT app_id, winget_id, version, architecture, installer_type, scope, url, sha256, size, arguments,
			return_codes, fetched_at
		FROM thirdparty_packages ORDER BY app_id COLLATE "C", fetched_at DESC, version COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []ThirdPartyPackage{}
	for rows.Next() {
		var pk ThirdPartyPackage
		var codes []byte
		if err := rows.Scan(&pk.AppID, &pk.WingetID, &pk.Version, &pk.Architecture, &pk.Type, &pk.Scope, &pk.URL,
			&pk.SHA256, &pk.Size, &pk.Arguments, &codes, &pk.FetchedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(codes, &pk.ReturnCodes); err != nil {
			return nil, fmt.Errorf("return_codes de %s %s: %w", pk.AppID, pk.Version, err)
		}
		pk.FetchedAt = pk.FetchedAt.UTC()
		list = append(list, pk)
	}
	return list, rows.Err()
}

// SaveThirdPartyPackage registra o instalador (memória).
func (m *Memory) SaveThirdPartyPackage(_ context.Context, pk ThirdPartyPackage) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.packages == nil {
		m.packages = map[string]ThirdPartyPackage{}
	}
	k := packageKey(pk)
	if old, ok := m.packages[k]; ok {
		if old.SHA256 != pk.SHA256 {
			return false, &PackageChangedError{Stored: old, SHA256: pk.SHA256}
		}
		old.FetchedAt = m.now()
		m.packages[k] = old
		return false, nil
	}
	pk.FetchedAt = m.now()
	if pk.ReturnCodes == nil {
		pk.ReturnCodes = map[int]string{}
	}
	m.packages[k] = pk
	return true, nil
}

// ThirdPartyPackages lista os instaladores guardados (memória).
func (m *Memory) ThirdPartyPackages(context.Context) ([]ThirdPartyPackage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]ThirdPartyPackage, 0, len(m.packages))
	for _, pk := range m.packages {
		list = append(list, pk)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.AppID != b.AppID {
			return a.AppID < b.AppID
		}
		if !a.FetchedAt.Equal(b.FetchedAt) {
			return a.FetchedAt.After(b.FetchedAt)
		}
		return a.Version < b.Version
	})
	return list, nil
}
