package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// A fonte do instalador de cada programa de terceiros (migração 0079). Sem linha: o winget,
// sem download automático. Quem confere o link e o tipo é o pacote tpfetch; aqui só se guarda.

// Os tipos de fonte.
const (
	SourceWinget     = "winget"
	SourceURL        = "url"
	SourceChocolatey = "chocolatey"
)

// InstallerSource é a fonte do instalador de um programa.
type InstallerSource struct {
	AppID string
	Kind  string // SourceWinget ou SourceURL
	// URLTemplate, Type, Arguments e ChecksumURL: o site do fabricante (Kind SourceURL).
	URLTemplate, Type, Arguments, ChecksumURL string
	// PackageID, URLVar e ChecksumVar: o Chocolatey (Kind SourceChocolatey): o pacote e,
	// opcional, as variáveis do script com o link e o SHA-256. Type e Arguments valem também.
	PackageID, URLVar, ChecksumVar string
	AutoFetch                      bool // baixar sozinho a versão de referência nova
	UpdatedBy                      string
	UpdatedAt                      time.Time
}

// ErrInstallerSource: a fonte fora do formato.
var ErrInstallerSource = errors.New("fonte do instalador inválida")

func (s InstallerSource) check() error {
	switch {
	case s.AppID == "" || len(s.AppID) > 64 || strings.TrimSpace(s.UpdatedBy) == "":
		return ErrInstallerSource
	case s.Kind != SourceWinget && s.Kind != SourceURL && s.Kind != SourceChocolatey:
		return ErrInstallerSource
	case s.Kind == SourceURL && (s.URLTemplate == "" || s.Type == ""):
		return ErrInstallerSource
	case s.Kind == SourceChocolatey && (s.PackageID == "" || s.Type == "" || len(s.PackageID) > 100 || len(s.URLVar) > 64 || len(s.ChecksumVar) > 64):
		return ErrInstallerSource
	case len(s.URLTemplate) > 1000 || len(s.ChecksumURL) > 1000 || len(s.Arguments) > 400:
		return ErrInstallerSource
	}
	return nil
}

// InstallerSources lista as fontes gravadas, por programa.
func (p *Postgres) InstallerSources(ctx context.Context) ([]InstallerSource, error) {
	rows, err := p.pool.Query(ctx, `SELECT app_id, kind, url_template, type, arguments, checksum_url, package_id, url_var, checksum_var,
		auto_fetch, updated_by, updated_at FROM thirdparty_sources ORDER BY app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstallerSource
	for rows.Next() {
		var s InstallerSource
		if err := rows.Scan(&s.AppID, &s.Kind, &s.URLTemplate, &s.Type, &s.Arguments, &s.ChecksumURL, &s.PackageID, &s.URLVar, &s.ChecksumVar,
			&s.AutoFetch, &s.UpdatedBy, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.UpdatedAt = s.UpdatedAt.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveInstallerSource grava (ou troca) a fonte do programa.
func (p *Postgres) SaveInstallerSource(ctx context.Context, s InstallerSource) error {
	if err := s.check(); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO thirdparty_sources (app_id, kind, url_template, type, arguments, checksum_url, package_id, url_var,
			checksum_var, auto_fetch, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
		ON CONFLICT (app_id) DO UPDATE SET kind = $2, url_template = $3, type = $4, arguments = $5, checksum_url = $6, package_id = $7,
			url_var = $8, checksum_var = $9, auto_fetch = $10, updated_by = $11, updated_at = now()`,
		s.AppID, s.Kind, s.URLTemplate, s.Type, s.Arguments, s.ChecksumURL, s.PackageID, s.URLVar, s.ChecksumVar, s.AutoFetch, s.UpdatedBy)
	return err
}

// DeleteInstallerSource volta o programa ao padrão (o winget, sem download automático).
func (p *Postgres) DeleteInstallerSource(ctx context.Context, app string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM thirdparty_sources WHERE app_id = $1`, app)
	return tag.RowsAffected() > 0, err
}

// InstallerSources (memória).
func (m *Memory) InstallerSources(_ context.Context) ([]InstallerSource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]InstallerSource, 0, len(m.tpSources))
	for _, s := range m.tpSources {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b InstallerSource) int { return strings.Compare(a.AppID, b.AppID) })
	return out, nil
}

// SaveInstallerSource (memória).
func (m *Memory) SaveInstallerSource(_ context.Context, s InstallerSource) error {
	if err := s.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tpSources == nil {
		m.tpSources = map[string]InstallerSource{}
	}
	s.UpdatedAt = m.now()
	m.tpSources[s.AppID] = s
	return nil
}

// DeleteInstallerSource (memória).
func (m *Memory) DeleteInstallerSource(_ context.Context, app string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tpSources[app]
	delete(m.tpSources, app)
	return ok, nil
}
