-- 0035: aprovação da TI no autoatendimento (Aula 18.3). A oferta pode exigir que a TI aprove
-- cada pedido: o pedido fica guardado, com a conta e os grupos de quem pediu (os do token,
-- no momento do pedido), até alguém aprovar (vira job, com o usuário como autor) ou recusar
-- (com o motivo). Sem decisão em 7 dias, expira.

ALTER TABLE selfservice_offers ADD COLUMN needs_approval boolean NOT NULL DEFAULT false;

CREATE TABLE selfservice_requests (
    id          bigserial   PRIMARY KEY,
    machine_id  text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    account     text        NOT NULL,
    groups      text[]      NOT NULL DEFAULT '{}',
    app_id      text        NOT NULL,
    action      text        NOT NULL,
    version     text        NOT NULL,
    state       text        NOT NULL DEFAULT 'aguardando',
    created_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz,
    decided_by  text        NOT NULL DEFAULT '',
    note        text        NOT NULL DEFAULT '',
    job_id      bigint,
    CHECK (state IN ('aguardando', 'aprovado', 'recusado', 'expirado'))
);

CREATE INDEX selfservice_requests_open_idx ON selfservice_requests (created_at) WHERE state = 'aguardando';
CREATE INDEX selfservice_requests_user_idx ON selfservice_requests (machine_id, lower(account), created_at DESC);
