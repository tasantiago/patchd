-- 0008: versões do macOS (gdmf, da Apple) e versões de segurança com CVEs (SOFA) (Aula 6.2).
--
-- As duas fontes são pequenas (dezenas e centenas de KB): cada sincronização que encontra
-- conteúdo novo substitui a fonte inteira, numa transação. O que diz "mudou" fica em
-- catalog_feeds, uma linha por fonte.

CREATE TABLE catalog_feeds (
    source     text        PRIMARY KEY COLLATE "C", -- ex.: apple-gdmf, apple-sofa
    hash       text        NOT NULL,                -- SHA-256 do corpo (gdmf) ou UpdateHash (SOFA)
    fetched_at timestamptz NOT NULL DEFAULT now()
);

-- O que a Apple publica para instalar (gdmf). Não diz o que tem suporte de segurança.
CREATE TABLE apple_versions (
    product_version    text    NOT NULL COLLATE "C", -- ex.: 26.7.1
    build              text    NOT NULL COLLATE "C", -- ex.: 25G241; o 27.0.1 tem dois
    extra              text    NOT NULL,             -- "(a)": melhoria de segurança em segundo plano
    prerequisite_build text    NOT NULL,
    posting_date       date,                         -- publicação no catálogo, não a data da versão
    expiration_date    date,
    devices            integer NOT NULL,
    PRIMARY KEY (product_version, build)
);

-- As versões de segurança de cada major, segundo o SOFA (que lê os boletins da Apple).
CREATE TABLE apple_releases (
    product_version text    PRIMARY KEY COLLATE "C",
    major           text    NOT NULL,                -- ex.: Tahoe 26
    major_number    text    NOT NULL COLLATE "C",    -- ex.: 26
    update_name     text    NOT NULL,
    builds          text[]  NOT NULL,                -- vazio nas versões antigas
    release_date    date    NOT NULL,
    security_info   text    NOT NULL,                -- URL do boletim da Apple
    unique_cves     integer NOT NULL
);
CREATE INDEX apple_releases_major_idx ON apple_releases (major_number, release_date);

CREATE TABLE apple_release_cves (
    product_version text    NOT NULL REFERENCES apple_releases (product_version) ON DELETE CASCADE,
    cve             text    NOT NULL COLLATE "C",
    exploited       boolean NOT NULL,
    in_kev          boolean NOT NULL,
    severity        text    NOT NULL,
    PRIMARY KEY (product_version, cve)
);
CREATE INDEX apple_release_cves_cve_idx ON apple_release_cves (cve);
