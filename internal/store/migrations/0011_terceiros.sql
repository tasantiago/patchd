-- 0011: versões de referência dos programas de terceiros (Aula 6.5).
--
-- Uma linha por regra do manifesto (programa × SO) com fonte: a versão mais nova que a
-- fonte informava na última sincronização. Uma fonte que falha não apaga a linha: fica a
-- última versão conhecida, com a data em que foi buscada.

CREATE TABLE thirdparty_versions (
    app        text        NOT NULL COLLATE "C", -- id da regra: google-chrome
    os         text        NOT NULL COLLATE "C", -- windows ou macos
    version    text        NOT NULL,             -- já normalizada: 155.0.8059.40
    source     text        NOT NULL,             -- chrome:win64, winget:7zip.7zip...
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (app, os)
);
