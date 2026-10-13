-- 0027: instaladores de terceiros guardados pelo servidor (Aula 14.2). Cada linha é um
-- instalador baixado do endereço do manifesto do winget e conferido pelo SHA-256; o
-- arquivo fica em PATCHD_CONTENT_DIR/pacotes/<sha256>.msi|.exe.
--
-- A chave é programa + versão + arquitetura + tipo. A mesma chave com outro SHA-256 é
-- recusada pelo servidor: a mesma versão com outro binário é sinal de alarme, não de
-- atualização (a aprovação da Aula 14.4 vai ser por programa e versão).

CREATE TABLE thirdparty_packages (
    app_id         text        NOT NULL CHECK (app_id <> ''),
    winget_id      text        NOT NULL CHECK (winget_id <> ''),
    version        text        NOT NULL CHECK (version <> ''),
    architecture   text        NOT NULL CHECK (architecture IN ('x64', 'x86', 'arm64', 'neutral')),
    installer_type text        NOT NULL CHECK (installer_type IN ('msi', 'wix', 'nullsoft', 'inno', 'burn', 'exe')),
    scope          text        NOT NULL DEFAULT '',
    url            text        NOT NULL CHECK (url LIKE 'https://%'),
    sha256         text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size           bigint      NOT NULL CHECK (size > 0),
    arguments      text        NOT NULL DEFAULT '',
    return_codes   jsonb       NOT NULL DEFAULT '{}',
    fetched_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (app_id, version, architecture, installer_type)
);
