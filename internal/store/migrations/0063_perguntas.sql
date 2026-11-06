-- 0063: perguntas ao parque. A pergunta (o sensor e o argumento, de um catálogo fechado) vira
-- um job "pergunta" por máquina; a resposta é o detalhe do resultado do job. Apagar a
-- pergunta apaga os jobs dela.

CREATE TABLE questions (
    id         bigserial   PRIMARY KEY,
    sensor     text        NOT NULL CHECK (sensor IN ('arquivo', 'processo', 'servico', 'registro', 'programa')),
    arg        text        NOT NULL CHECK (length(arg) BETWEEN 1 AND 300),
    name       text        NOT NULL DEFAULT '' CHECK (length(name) <= 128),
    target     text        NOT NULL CHECK (length(target) BETWEEN 1 AND 100),
    machines   integer     NOT NULL CHECK (machines >= 0),
    created_by text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX questions_created ON questions (created_at DESC);

ALTER TABLE jobs ADD COLUMN question_id bigint REFERENCES questions (id) ON DELETE CASCADE;
CREATE INDEX jobs_question_idx ON jobs (question_id) WHERE question_id IS NOT NULL;
