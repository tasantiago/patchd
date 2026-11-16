-- 0073: fontes novas dos programas de terceiros.
--
-- A segunda fonte da versão (o Evergreen, que lê no fabricante) fica ao lado da principal:
-- quando ela está à frente, a fonte principal (o winget-pkgs) ainda não acompanhou.
ALTER TABLE thirdparty_versions
    ADD COLUMN check_version text NOT NULL DEFAULT '',
    ADD COLUMN check_source  text NOT NULL DEFAULT '';

-- Os ciclos de suporte do endoflife.date, por produto: o fim do suporte e a versão mais nova
-- de cada linha (3.12, 22, 8.0). Uma sincronização que falha não apaga o que estava guardado.
CREATE TABLE thirdparty_lifecycles (
    product    text        NOT NULL COLLATE "C" CHECK (length(product) BETWEEN 1 AND 64),
    cycle      text        NOT NULL COLLATE "C" CHECK (length(cycle) BETWEEN 1 AND 40),
    eol_from   date,
    is_eol     boolean     NOT NULL,
    latest     text        NOT NULL DEFAULT '' CHECK (length(latest) <= 64),
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (product, cycle)
);
