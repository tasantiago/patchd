-- 0046: bundles de script (Aula 23.8). O admin sobe um ZIP com o executável, o .ini e o
-- script que começa a execução; escolhe o ponto de entrada e as máquinas, e cada máquina
-- recebe um job 'script' assinado que carrega o SHA-256 do ZIP.
--
-- O ZIP em si NÃO fica no banco: fica no diretório de conteúdo do servidor, em
-- bundles/<sha256>.zip, como os instaladores de terceiros. Aqui ficam só os metadados e a
-- lista de arquivos, para a tela montar o seletor de ponto de entrada sem reabrir o ZIP.

CREATE TABLE script_bundles (
    id          bigserial   PRIMARY KEY,
    name        text        NOT NULL CHECK (name <> '' AND length(name) <= 64),
    sha256      text        NOT NULL UNIQUE CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size        bigint      NOT NULL CHECK (size > 0),
    os          text        NOT NULL DEFAULT '' CHECK (os IN ('', 'windows', 'unix')),
    files       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    note        text        NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    uploaded_at timestamptz NOT NULL DEFAULT now(),
    uploaded_by text        NOT NULL DEFAULT ''
);

CREATE INDEX script_bundles_name ON script_bundles (name);
