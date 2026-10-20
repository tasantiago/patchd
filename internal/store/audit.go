package store

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/texto"
)

// Trilha de auditoria (Aula 15.13): quem fez o quê, quando, de onde. Só se acrescenta.

// AuditEvent é uma ação registrada.
type AuditEvent struct {
	ID        int64
	At        time.Time
	Username  string
	Source    string // origem da sessão: local, ad, oidc ("" quando não há sessão)
	Action    string // ex.: patch.aprovar
	Target    string
	Detail    string
	Remote    string
	RequestID string
}

// Limites dos textos: o evento é um resumo; o detalhe completo, quando houver, está no log
// com o mesmo request_id.
const (
	maxAuditText   = 200
	maxAuditDetail = 1000
	// MaxAuditPage: eventos por consulta.
	MaxAuditPage = 500
)

// AuditFilter escolhe os eventos: todos os campos são opcionais.
type AuditFilter struct {
	Username string    // exato
	Action   string    // exato, ou prefixo terminado em "." (ex.: "patch.")
	Since    time.Time // a partir de
	BeforeID int64     // paginação: só eventos com id menor
	Limit    int       // até MaxAuditPage; 0 = 100
}

// clipText limpa e corta. Aula 20.4b (achado F8): o caminho de uma URL com %00 ou UTF-8
// inválido ia cru para o "acesso.negado", o PostgreSQL recusava o INSERT e a tentativa
// ficava fora da trilha. Agora o NUL e o UTF-8 inválido viram "�" (texto.Clip): o evento
// entra, com o pedido do jeito que dá para mostrar.
func clipText(s string, n int) string { return texto.Clip(s, n) }

func (e *AuditEvent) clip() {
	e.Username, e.Source, e.Action = clipText(e.Username, maxAuditText), clipText(e.Source, maxAuditText), clipText(e.Action, maxAuditText)
	e.Target, e.Remote, e.RequestID = clipText(e.Target, maxAuditText), clipText(e.Remote, maxAuditText), clipText(e.RequestID, maxAuditText)
	e.Detail = clipText(e.Detail, maxAuditDetail)
}

func (f AuditFilter) limit() int {
	switch {
	case f.Limit <= 0:
		return 100
	case f.Limit > MaxAuditPage:
		return MaxAuditPage
	}
	return f.Limit
}

// RecordAudit grava o evento. At vazio: agora.
func (p *Postgres) RecordAudit(ctx context.Context, e AuditEvent) error {
	e.clip()
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO audit_events (at, username, source, action, target, detail, remote, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.At, e.Username, e.Source, e.Action, e.Target, e.Detail, e.Remote, e.RequestID)
	return err
}

// AuditEvents lista os eventos do filtro, do mais novo para o mais antigo.
func (p *Postgres) AuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	action, prefix := f.Action, strings.HasSuffix(f.Action, ".")
	if prefix {
		action = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(action) + "%"
	}
	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	rows, err := p.pool.Query(ctx, `
		SELECT id, at, username, source, action, target, detail, remote, request_id
		FROM audit_events
		WHERE ($1 = '' OR username = $1)
		  AND ($2 = '' OR (CASE WHEN $3 THEN action LIKE $2 ELSE action = $2 END))
		  AND ($4::timestamptz IS NULL OR at >= $4)
		  AND ($5 = 0 OR id < $5)
		ORDER BY id DESC
		LIMIT $6`, f.Username, action, prefix, since, f.BeforeID, f.limit())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEvent, error) {
		var e AuditEvent
		err := r.Scan(&e.ID, &e.At, &e.Username, &e.Source, &e.Action, &e.Target, &e.Detail, &e.Remote, &e.RequestID)
		e.At = e.At.UTC()
		return e, err
	})
}

// RecordAudit na memória.
func (m *Memory) RecordAudit(ctx context.Context, e AuditEvent) error {
	e.clip()
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.At.IsZero() {
		e.At = m.now()
	}
	m.auditSeq++
	e.ID = m.auditSeq
	m.audit = append(m.audit, e)
	return nil
}

// AuditEvents na memória.
func (m *Memory) AuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []AuditEvent{}
	for _, e := range slices.Backward(m.audit) {
		switch {
		case f.Username != "" && e.Username != f.Username,
			f.Action != "" && strings.HasSuffix(f.Action, ".") && !strings.HasPrefix(e.Action, f.Action),
			f.Action != "" && !strings.HasSuffix(f.Action, ".") && e.Action != f.Action,
			!f.Since.IsZero() && e.At.Before(f.Since),
			f.BeforeID != 0 && e.ID >= f.BeforeID:
			continue
		}
		out = append(out, e)
		if len(out) == f.limit() {
			break
		}
	}
	return out, nil
}
