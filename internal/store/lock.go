package store

import (
	"context"
	"time"
)

// Travas consultivas do PostgreSQL (Aula 8.5b): um trabalho periódico que não pode rodar
// em dois servidores ao mesmo tempo pega a trava antes; quem não consegue pula a vez.
// A trava é da sessão: fica com a conexão separada para ela até o fim do trabalho.
// https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS

// LockCampaigns é a chave da trava das campanhas.
const LockCampaigns int64 = 0x7061_7463_6864_0001 // "patchd" + 1

// WithAdvisoryLock roda fn só se conseguir a trava key; devolve false se outra sessão
// (outro servidor) está com ela.
func (p *Postgres) WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil || !ok {
		return false, err
	}
	defer func() {
		// Sem o contexto do pedido: a trava tem de ser solta mesmo no encerramento.
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, key)
	}()
	return true, fn(ctx)
}
