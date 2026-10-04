package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Postgres guarda os relatórios no PostgreSQL. Cumpre a mesma interface da Memory.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres usa um pool já conectado e com as migrations aplicadas.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Connect abre o pool e espera o banco responder por até wait (o PostgreSQL pode estar
// subindo junto com o servidor). Erro de configuração da URL não é repetido.
func Connect(ctx context.Context, url string, wait time.Duration) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// A mensagem do pgx pode conter a URL inteira, com a senha: não repassar.
		return nil, errors.New("URL do banco inválida")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pool do banco: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("banco indisponível após %s: %w", wait, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// upsertMachine registra a máquina (ou atualiza o último contato) e trava a linha dela
// até o fim da transação: dois relatórios simultâneos da mesma máquina são serializados.
// Devolve o hash do inventário atual (nil se ainda não houver).
//
// São dois comandos de propósito. No READ COMMITTED, cada comando enxerga os dados
// confirmados no instante em que COMEÇA. Uma leitura no mesmo comando do upsert usaria a
// foto tirada antes de esperar a trava e não veria o inventário que a outra transação
// acabou de gravar. O SELECT seguinte começa depois da trava e enxerga.
func upsertMachine(ctx context.Context, tx pgx.Tx, id string) (*string, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO machines (id) VALUES ($1)
		ON CONFLICT (id) DO UPDATE SET last_seen_at = now()`, id); err != nil {
		return nil, err
	}
	var current *string
	err := tx.QueryRow(ctx, `
		SELECT r.hash FROM machines m
		LEFT JOIN inventory_reports r ON r.id = m.current_inventory_id
		WHERE m.id = $1`, id).Scan(&current)
	return current, err
}

// SaveInventory grava o relatório se o hash for diferente do atual da máquina.
func (p *Postgres) SaveInventory(ctx context.Context, id string, rep protocol.InventoryReport) (bool, error) {
	body, err := json.Marshal(rep)
	if err != nil {
		return false, err
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	current, err := upsertMachine(ctx, tx, id)
	if err != nil {
		return false, fmt.Errorf("máquina: %w", err)
	}
	if current != nil && *current == rep.Hash {
		// Nada mudou: fica só o último contato.
		return false, tx.Commit(ctx)
	}

	var reportID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_reports
			(machine_id, hash, schema_version, agent_version, collected_at, hostname, os_family, os_name, os_version, report)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`,
		id, rep.Hash, rep.SchemaVersion, rep.AgentVersion, rep.CollectedAt,
		rep.OS.Hostname, rep.OS.Family, rep.OS.Name, rep.OS.Version, body,
	).Scan(&reportID)
	if err != nil {
		return false, fmt.Errorf("inventário: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE machines SET current_inventory_id = $1 WHERE id = $2", reportID, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// LatestInventory devolve o inventário atual da máquina.
func (p *Postgres) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	var rep protocol.InventoryReport
	found, err := p.latest(ctx, `
		SELECT r.report FROM machines m
		JOIN inventory_reports r ON r.id = m.current_inventory_id
		WHERE m.id = $1`, id, &rep)
	return rep, found, err
}

// SaveScan grava a busca e a torna a atual da máquina.
func (p *Postgres) SaveScan(ctx context.Context, id string, rep protocol.PatchScanReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	var reboot *bool
	if rep.Reboot != nil {
		reboot = rep.Reboot.Pending
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := upsertMachine(ctx, tx, id); err != nil {
		return fmt.Errorf("máquina: %w", err)
	}
	var scanID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO scan_reports (machine_id, schema_version, agent_version, scanned_at, reboot_pending, report)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		id, rep.SchemaVersion, rep.AgentVersion, rep.ScannedAt, reboot, body,
	).Scan(&scanID)
	if err != nil {
		return fmt.Errorf("busca: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE machines SET current_scan_id = $1 WHERE id = $2", scanID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LatestScan devolve a busca atual da máquina.
func (p *Postgres) LatestScan(ctx context.Context, id string) (protocol.PatchScanReport, bool, error) {
	var rep protocol.PatchScanReport
	found, err := p.latest(ctx, `
		SELECT r.report FROM machines m
		JOIN scan_reports r ON r.id = m.current_scan_id
		WHERE m.id = $1`, id, &rep)
	return rep, found, err
}

// latest lê um relatório JSONB e o decodifica em dst; sem linha = não encontrado.
func (p *Postgres) latest(ctx context.Context, query, id string, dst any) (bool, error) {
	var body []byte
	err := p.pool.QueryRow(ctx, query, id).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return false, fmt.Errorf("relatório guardado ilegível: %w", err)
	}
	return true, nil
}

// Machines resume as máquinas em ordem de ID. COLLATE "C" ordena byte a byte, como o Go,
// sem depender do locale do banco.
func (p *Postgres) Machines(ctx context.Context) ([]protocol.MachineSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, m.last_seen_at, m.agent_version,
		       i.hostname, i.os_name, i.os_version, i.hash, i.received_at,
		       s.received_at, s.reboot_pending
		FROM machines m
		LEFT JOIN inventory_reports i ON i.id = m.current_inventory_id
		LEFT JOIN scan_reports s      ON s.id = m.current_scan_id
		ORDER BY m.id COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []protocol.MachineSummary{}
	for rows.Next() {
		var s protocol.MachineSummary
		var hostname, osName, osVersion, hash *string
		var invAt, scanAt *time.Time
		if err := rows.Scan(&s.ID, &s.LastSeenAt, &s.AgentVersion, &hostname, &osName, &osVersion, &hash, &invAt, &scanAt, &s.RebootPending); err != nil {
			return nil, err
		}
		s.LastSeenAt = s.LastSeenAt.UTC()
		s.Hostname, s.OSName, s.OSVersion, s.InventoryHash = deref(hostname), deref(osName), deref(osVersion), deref(hash)
		s.InventoryChangedAt, s.ScanReceivedAt = utc(invAt), utc(scanAt)
		list = append(list, s)
	}
	return list, rows.Err()
}

// CheckIn registra o contato periódico da máquina e diz se o inventário atual dela tem o
// hash informado. Não há corrida que importe: no pior caso, o agente reenvia um
// inventário que o servidor acabou de receber, e o envio volta "unchanged".
func (p *Postgres) CheckIn(ctx context.Context, id, agentVersion, inventoryHash string) (bool, error) {
	var current *string
	err := p.pool.QueryRow(ctx, `
		UPDATE machines m SET last_seen_at = now(), agent_version = $2
		WHERE m.id = $1
		RETURNING (SELECT r.hash FROM inventory_reports r WHERE r.id = m.current_inventory_id)`,
		id, agentVersion).Scan(&current)
	if err != nil {
		return false, err
	}
	return current != nil && *current == inventoryHash, nil
}

// PruneScans apaga as buscas recebidas antes de before, exceto a atual de cada máquina.
// Devolve quantas foram apagadas.
func (p *Postgres) PruneScans(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `
		DELETE FROM scan_reports s
		WHERE s.received_at < $1
		  AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.current_scan_id = s.id)`, before)
	return tag.RowsAffected(), err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
