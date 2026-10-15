-- 0032: trilha de auditoria (Aula 15.13).
--
-- O log do servidor diz o que aconteceu, mas vai para a saída do processo: gira, some com o
-- container, não se consulta por usuário nem por período. A trilha guarda no banco as ações
-- que mudam alguma coisa (aprovações, jobs, sessões, usuários, tokens, aposentadorias), os
-- logins e as exportações, com quem, quando, de onde e o request_id que liga ao log.
--
-- Só se acrescenta: UPDATE é recusado por gatilho, inclusive para o dono da tabela. Apagar
-- fica para a retenção, que é decisão explícita (e também vai para a trilha).
CREATE TABLE audit_events (
    id         bigserial   PRIMARY KEY,
    at         timestamptz NOT NULL DEFAULT now(),
    username   text        NOT NULL,
    source     text        NOT NULL DEFAULT '',  -- origem da sessão: local, ad, oidc
    action     text        NOT NULL,             -- ex.: patch.aprovar, job.criar, sessao.encerrar
    target     text        NOT NULL DEFAULT '',  -- o que a ação atingiu: máquina, job, item, usuário
    detail     text        NOT NULL DEFAULT '',
    remote     text        NOT NULL DEFAULT '',  -- IP de quem fez
    request_id text        NOT NULL DEFAULT ''
);

CREATE INDEX audit_events_at_idx ON audit_events (at DESC, id DESC);
CREATE INDEX audit_events_user_idx ON audit_events (username, at DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, at DESC);

CREATE FUNCTION audit_events_sem_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a trilha de auditoria não se altera (só se acrescenta)';
END;
$$;

CREATE TRIGGER audit_events_sem_update BEFORE UPDATE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_sem_update();
