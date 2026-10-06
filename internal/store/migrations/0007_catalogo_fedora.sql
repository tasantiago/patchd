-- 0007: updates de segurança do Fedora, lidos do Bodhi (Aula 6.2).
--
-- Um update é a unidade gravada: cada sincronização regrava, numa transação por versão
-- do Fedora, todos os updates que vieram, com os builds (pacote fonte e EVR) e as CVEs.
-- A conformidade compara o pacote fonte instalado com o build do update pelas regras do
-- rpm (época, versão e release; Aula 6.4).

CREATE TABLE fedora_updates (
    alias        text        PRIMARY KEY COLLATE "C", -- ex.: FEDORA-2026-35b989661c
    release      text        NOT NULL COLLATE "C",    -- versão do Fedora: F44
    title        text        NOT NULL,
    severity     text        NOT NULL,                -- urgent, high, medium, low, unspecified
    date_pushed  timestamptz,                         -- o que a sincronização incremental usa
    date_stable  timestamptz,
    -- Algum título de bug de segurança veio cortado com "...": a lista de CVEs do update
    -- está incompleta (o Bodhi não tem campo de CVE).
    cves_partial boolean     NOT NULL,
    fetched_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fedora_updates_release_pushed_idx ON fedora_updates (release, date_pushed);

CREATE TABLE fedora_update_builds (
    alias       text    NOT NULL REFERENCES fedora_updates (alias) ON DELETE CASCADE,
    nvr         text    NOT NULL COLLATE "C",
    package     text    NOT NULL COLLATE "C", -- pacote fonte: chromium
    epoch       integer NOT NULL,
    version     text    NOT NULL COLLATE "C", -- 154.0.8037.92
    rpm_release text    NOT NULL COLLATE "C", -- 1.fc44 (o release do rpm, não a versão do Fedora)
    PRIMARY KEY (alias, nvr)
);
CREATE INDEX fedora_update_builds_package_idx ON fedora_update_builds (package);

CREATE TABLE fedora_update_cves (
    alias text NOT NULL REFERENCES fedora_updates (alias) ON DELETE CASCADE,
    cve   text NOT NULL COLLATE "C",
    PRIMARY KEY (alias, cve)
);
CREATE INDEX fedora_update_cves_cve_idx ON fedora_update_cves (cve);
