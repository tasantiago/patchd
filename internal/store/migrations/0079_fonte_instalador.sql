-- A fonte do instalador de cada programa de terceiros (docs/operacao.md, "Fonte do
-- instalador"): o winget (o padrão, sem linha) ou o site do fabricante (o link com {versao},
-- o tipo, os argumentos e, opcional, o arquivo de SHA-256), e o download automático da
-- versão de referência.
CREATE TABLE thirdparty_sources (
    app_id       text PRIMARY KEY,
    kind         text NOT NULL CHECK (kind IN ('winget', 'url')),
    url_template text NOT NULL DEFAULT '',
    type         text NOT NULL DEFAULT '',
    arguments    text NOT NULL DEFAULT '',
    checksum_url text NOT NULL DEFAULT '',
    auto_fetch   boolean NOT NULL DEFAULT false,
    updated_by   text NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
