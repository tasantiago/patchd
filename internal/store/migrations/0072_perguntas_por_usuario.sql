-- 0072: perguntas ao parque que olham cada usuário da máquina (as extensões dos navegadores,
-- um arquivo na pasta do perfil). A resposta de uma máquina é uma lista, que não cabe no
-- detalhe do job: os itens ficam em question_items, e a tela junta o parque por item.

ALTER TABLE questions DROP CONSTRAINT questions_sensor_check;
ALTER TABLE questions ADD CONSTRAINT questions_sensor_check
    CHECK (sensor IN ('arquivo', 'processo', 'servico', 'registro', 'programa', 'extensoes', 'arquivo-usuario'));
-- "extensoes" sem filtro pergunta por todas: o argumento pode vir vazio.
ALTER TABLE questions DROP CONSTRAINT questions_arg_check;
ALTER TABLE questions ADD CONSTRAINT questions_arg_check CHECK (length(arg) <= 300);
-- Máquinas do alvo que ficaram de fora porque o agente é anterior à pergunta.
ALTER TABLE questions ADD COLUMN skipped integer NOT NULL DEFAULT 0 CHECK (skipped >= 0);

CREATE TABLE question_items (
    question_id bigint NOT NULL REFERENCES questions (id) ON DELETE CASCADE,
    machine_id  text   NOT NULL,
    key         text   NOT NULL CHECK (length(key) BETWEEN 1 AND 100),  -- o que junta o item no parque (o ID da extensão)
    name        text   NOT NULL CHECK (length(name) <= 150),
    version     text   NOT NULL DEFAULT '' CHECK (length(version) <= 60),
    usuario     text   NOT NULL DEFAULT '' CHECK (length(usuario) <= 100),
    origem      text   NOT NULL DEFAULT '' CHECK (length(origem) <= 40),    -- o navegador
    lugar       text   NOT NULL DEFAULT '' CHECK (length(lugar) <= 300),    -- o perfil do navegador; o caminho
    nota        text   NOT NULL DEFAULT '' CHECK (length(nota) <= 300)
);
CREATE INDEX question_items_key_idx ON question_items (question_id, key);
CREATE INDEX question_items_machine_idx ON question_items (question_id, machine_id);
