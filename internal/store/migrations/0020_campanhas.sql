-- 0020: campanhas (Aula 8.5, RF-25). Uma campanha é o mesmo pedido (rescan, ou update de
-- certas fontes) passando pelos anéis em ordem: o servidor cria os jobs do anel seguinte
-- sozinho quando todos os jobs do anel atual terminaram com sucesso e o tempo de
-- observação passou. Qualquer job sem sucesso pausa a campanha até o admin decidir.
--
-- current_idx é a posição em rings do anel atual; launched_idx, a do último anel cujos jobs
-- já foram criados (-1: nenhum). accepted_idx: o admin retomou aceitando as falhas desse
-- anel. ring_done_at: quando o anel atual terminou (começo da observação).

CREATE TABLE campaigns (
    id            bigserial   PRIMARY KEY,
    name          text        NOT NULL,
    type          text        NOT NULL,
    sources       text[]      NOT NULL DEFAULT '{}',
    rings         smallint[]  NOT NULL,
    use_window    boolean     NOT NULL,
    observe_sec   integer     NOT NULL CHECK (observe_sec >= 0),
    ttl_sec       integer     NOT NULL CHECK (ttl_sec > 0),
    state         text        NOT NULL CHECK (state IN ('em_andamento', 'pausada', 'concluida', 'cancelada')),
    current_idx   smallint    NOT NULL DEFAULT 0,
    launched_idx  smallint    NOT NULL DEFAULT -1,
    accepted_idx  smallint    NOT NULL DEFAULT -1,
    ring_done_at  timestamptz,
    detail        text        NOT NULL DEFAULT '',
    created_by    text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE jobs
    ADD COLUMN campaign_id  bigint REFERENCES campaigns (id),
    ADD COLUMN campaign_idx smallint;

CREATE INDEX jobs_campaign_idx ON jobs (campaign_id, campaign_idx) WHERE campaign_id IS NOT NULL;
