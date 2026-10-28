-- 0044: a localização física dos equipamentos, do patrimônio (Aula 23.6). O admin importa um
-- CSV do sistema de patrimônio (prédio, sala, unidade, tombo, descrição do bem) em
-- Configuração › Patrimônio; cada importação SUBSTITUI a anterior inteira (o arquivo é a
-- fonte, não um complemento). O painel liga a máquina ao tombo pelo nome: os dígitos do
-- hostname, sem os zeros à esquerda, iguais ao tombo.
--
-- O tombo fica sem zeros à esquerda: 088594 e 88594 são o mesmo bem.

CREATE TABLE asset_locations (
    tombo       text PRIMARY KEY CHECK (tombo ~ '^[1-9][0-9]{0,19}$'),
    building    text NOT NULL DEFAULT '' CHECK (length(building) <= 200),
    room        text NOT NULL DEFAULT '' CHECK (length(room) <= 200),
    unit        text NOT NULL DEFAULT '' CHECK (length(unit) <= 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    subgroup    text NOT NULL DEFAULT '' CHECK (length(subgroup) <= 200)
);

-- O histórico das importações: quem, quando, o arquivo e as contagens.
CREATE TABLE asset_imports (
    id          bigserial   PRIMARY KEY,
    imported_at timestamptz NOT NULL DEFAULT now(),
    imported_by text        NOT NULL CHECK (imported_by <> ''),
    file_name   text        NOT NULL DEFAULT '' CHECK (length(file_name) <= 200),
    rows_kept   integer     NOT NULL CHECK (rows_kept >= 0),
    rows_skipped integer    NOT NULL DEFAULT 0 CHECK (rows_skipped >= 0),
    duplicates  integer     NOT NULL DEFAULT 0 CHECK (duplicates >= 0)
);
