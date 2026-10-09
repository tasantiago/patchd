-- 0018: a fila de jobs (Aula 8.1, RF-08 e RF-25).
--
-- O job é guardado já assinado (payload + signature): o servidor entrega exatamente os
-- bytes que a chave de jobs assinou. Alterar uma linha não faz o agente aceitar o job
-- alterado. As colunas ao lado (tipo, máquina, prazo) servem para listar e filtrar; o que
-- vale para o agente é o payload.

CREATE TABLE jobs (
    id           bigserial   PRIMARY KEY,
    machine_id   text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE, -- mesma colação de machines.id
    type         text        NOT NULL,
    state        text        NOT NULL CHECK (state IN ('pendente', 'entregue', 'concluido', 'falhou', 'rejeitado', 'cancelado', 'expirado')),
    payload      text        NOT NULL,
    signature    text        NOT NULL,
    created_by   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    not_after    timestamptz NOT NULL,
    delivered_at timestamptz,
    finished_at  timestamptz,
    detail       text        NOT NULL DEFAULT ''
);

-- Os jobs em aberto de cada máquina: o que o check-in procura.
CREATE INDEX jobs_open_idx ON jobs (machine_id, id) WHERE state IN ('pendente', 'entregue');
CREATE INDEX jobs_created_idx ON jobs (created_at DESC);
