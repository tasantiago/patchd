-- 0012: registro das sincronizações do catálogo (Aula 6.6).
--
-- Uma linha por fonte em cada execução, agendada pelo servidor ou manual (catalog sync).
-- É o que responde "quando cada fonte foi atualizada com sucesso pela última vez" para o
-- catalog status e para o aviso de catálogo atrasado no compliance.

CREATE TABLE catalog_runs (
    id          bigserial   PRIMARY KEY,
    source      text        NOT NULL COLLATE "C", -- msrc, ubuntu, fedora, apple, kev, terceiros
    trigger     text        NOT NULL,             -- agendada ou manual
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    ok          boolean     NOT NULL,
    detail      text        NOT NULL              -- o resumo da fonte, ou o erro
);
CREATE INDEX catalog_runs_source_idx ON catalog_runs (source, finished_at DESC);
