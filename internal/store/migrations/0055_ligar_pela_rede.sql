-- 0055: os pedidos de ligar pela rede (docs/wake-on-lan.md, Aula 22.2). Um pedido do painel vira
-- um job wake_on_lan para cada repetidor (agente ligado da mesma sub-rede); a linha guarda os
-- MACs e a rede pedidos, os jobs, quem mandou o pacote primeiro e quando a máquina respondeu
-- (o primeiro check-in dela depois do pacote).

CREATE TABLE wake_requests (
    id           bigserial   PRIMARY KEY,
    machine_id   text        NOT NULL REFERENCES machines (id),
    requested_at timestamptz NOT NULL,
    requested_by text        NOT NULL,
    network      text        NOT NULL,
    macs         text[]      NOT NULL,
    relays       text[]      NOT NULL,
    job_ids      bigint[]    NOT NULL,
    sent_at      timestamptz,
    sent_by      text,
    answered_at  timestamptz
);
CREATE INDEX wake_requests_machine ON wake_requests (machine_id, requested_at DESC);
CREATE INDEX wake_requests_jobs ON wake_requests USING gin (job_ids);
