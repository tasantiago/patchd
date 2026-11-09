package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Travas consultivas do PostgreSQL (Aula 8.5b): um trabalho periódico que não pode rodar
// em dois servidores ao mesmo tempo pega a trava antes; quem não consegue pula a vez.
// A trava é da sessão: fica com a conexão separada para ela até o fim do trabalho.
// https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS

// LockCampaigns é a chave da trava das campanhas.
const LockCampaigns int64 = 0x7061_7463_6864_0001 // "patchd" + 1

// LockNotify é a chave da trava das notificações por e-mail (Aula 18.6).
const LockNotify int64 = 0x7061_7463_6864_0002

// WithAdvisoryLock roda fn só se conseguir a trava key; devolve false se outra sessão
// (outro servidor) está com ela.
func (p *Postgres) WithAdvisoryLock(ctx context.Context, key int64, fn func(context.Context) error) (bool, error) {
	// Conexão própria, fora do pool: ela só segura a trava, enquanto fn trabalha pelo pool. No
	// pool, eram duas vagas presas por trabalho (a da trava e a do trabalho).
	conn, err := p.sideConn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(context.Background()) }()
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

// sideConn abre uma conexão fora do pool, com a mesma configuração (endereço, TLS, senha): para
// o que fica preso a uma sessão (LISTEN, trava de sessão) sem tirar vaga do pool. Quem chama
// fecha. Conta no max_connections do PostgreSQL, não no pool_max_conns.
func (p *Postgres) sideConn(ctx context.Context) (*pgx.Conn, error) {
	return pgx.ConnectConfig(ctx, p.pool.Config().ConnConfig.Copy())
}
