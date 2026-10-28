package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
)

// Exclusão de uma versão guardada (Aula 23.3): o painel apaga os instaladores de uma versão
// que não serve mais. Três coisas seguram a versão: a aprovação (é uma decisão registrada;
// revogar é o passo consciente antes), um job thirdparty em aberto com um dos instaladores
// (o agente ainda vai baixá-lo) e uma campanha em andamento ou pausada da versão.

// ErrPackageApproved: a versão está aprovada.
var ErrPackageApproved = errors.New("a versão está aprovada: revogue a aprovação antes de excluir")

// PackageInUseError: jobs em aberto ou campanhas não encerradas usam a versão.
type PackageInUseError struct{ Jobs, Campaigns int }

func (e *PackageInUseError) Error() string {
	return fmt.Sprintf("a versão está em uso: %d job(s) em aberto e %d campanha(s) não encerrada(s)", e.Jobs, e.Campaigns)
}

// DeleteThirdPartyVersion apaga os registros dos instaladores da versão e devolve o que saiu
// (vazio: não havia nada). Os arquivos ficam por conta de quem chama: só ele sabe se outro
// registro ainda usa o mesmo SHA-256.
func (p *Postgres) DeleteThirdPartyVersion(ctx context.Context, app, version string) ([]ThirdPartyPackage, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var approved bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM thirdparty_approvals WHERE app_id = $1 AND version = $2)`,
		app, version).Scan(&approved); err != nil {
		return nil, err
	}
	if approved {
		return nil, ErrPackageApproved
	}
	// FOR UPDATE: duas exclusões da mesma versão ao mesmo tempo não contam a mesma linha.
	rows, err := tx.Query(ctx, `SELECT sha256 FROM thirdparty_packages WHERE app_id = $1 AND version = $2 FOR UPDATE`, app, version)
	if err != nil {
		return nil, err
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(shas) == 0 {
		return nil, nil
	}
	var inUse PackageInUseError
	// O payload é o JSON assinado do job; os parâmetros do thirdparty levam o SHA-256.
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE type = $1 AND state IN ('pendente', 'entregue') AND (payload::jsonb #>> '{params,sha256}') = ANY($2)`,
		jobs.TypeThirdParty, shas).Scan(&inUse.Jobs); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM campaigns WHERE app = $1 AND version = $2 AND state IN ($3, $4)`,
		app, version, campaign.StateRunning, campaign.StatePaused).Scan(&inUse.Campaigns); err != nil {
		return nil, err
	}
	if inUse.Jobs > 0 || inUse.Campaigns > 0 {
		return nil, &inUse
	}
	rows, err = tx.Query(ctx, `
		DELETE FROM thirdparty_packages WHERE app_id = $1 AND version = $2
		RETURNING app_id, winget_id, version, architecture, installer_type, sha256, size`, app, version)
	if err != nil {
		return nil, err
	}
	gone, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ThirdPartyPackage, error) {
		var pk ThirdPartyPackage
		err := r.Scan(&pk.AppID, &pk.WingetID, &pk.Version, &pk.Architecture, &pk.Type, &pk.SHA256, &pk.Size)
		return pk, err
	})
	if err != nil {
		return nil, err
	}
	return gone, tx.Commit(ctx)
}

// DeleteThirdPartyVersion (memória): as mesmas regras.
func (m *Memory) DeleteThirdPartyVersion(_ context.Context, app, version string) ([]ThirdPartyPackage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tpApprovals[TPApprovalKey(app, version)]; ok {
		return nil, ErrPackageApproved
	}
	var shas []string
	for _, pk := range m.packages {
		if pk.AppID == app && pk.Version == version {
			shas = append(shas, pk.SHA256)
		}
	}
	if len(shas) == 0 {
		return nil, nil
	}
	var inUse PackageInUseError
	for _, j := range m.jobs {
		if j.Type != jobs.TypeThirdParty || (j.State != "pendente" && j.State != "entregue") {
			continue
		}
		var job struct {
			Params struct {
				SHA256 string `json:"sha256"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(j.signed.Payload), &job) == nil && slices.Contains(shas, job.Params.SHA256) {
			inUse.Jobs++
		}
	}
	for _, c := range m.campaigns {
		if c.App == app && c.Version == version && (c.State == campaign.StateRunning || c.State == campaign.StatePaused) {
			inUse.Campaigns++
		}
	}
	if inUse.Jobs > 0 || inUse.Campaigns > 0 {
		return nil, &inUse
	}
	var gone []ThirdPartyPackage
	for k, pk := range m.packages {
		if pk.AppID == app && pk.Version == version {
			gone = append(gone, pk)
			delete(m.packages, k)
		}
	}
	return gone, nil
}
