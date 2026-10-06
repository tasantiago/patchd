-- 0005: avisos de segurança do Ubuntu (USN), lidos no formato OSV (Aula 6.2).
--
-- Um aviso é a unidade de sincronização: quando o modified_id.csv do OSV traz uma data
-- diferente da guardada, o aviso inteiro é apagado e gravado de novo, numa transação.
-- A conformidade compara a versão do pacote fonte instalado com fixed_version, pelas
-- regras do dpkg (Aula 6.4); por isso não há colunas de binários: o inventário já traz,
-- para cada binário, o pacote fonte e a versão dele.

CREATE TABLE ubuntu_usns (
    id         text        PRIMARY KEY COLLATE "C", -- ex.: USN-8861-1
    summary    text        NOT NULL,
    published  timestamptz NOT NULL,
    -- A data do modified_id.csv, exatamente como veio (com nanossegundos). É o que a
    -- sincronização compara; como timestamptz, perderia os nanossegundos e nunca mais
    -- seria igual à da lista.
    modified   text        NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ubuntu_usn_fixes (
    usn_id         text NOT NULL REFERENCES ubuntu_usns (id) ON DELETE CASCADE,
    ecosystem      text NOT NULL COLLATE "C", -- ex.: Ubuntu:26.04:LTS, Ubuntu:Pro:20.04:LTS
    source_package text NOT NULL COLLATE "C",
    fixed_version  text NOT NULL COLLATE "C", -- como veio, com a época: 1:3.5.17-1ubuntu0.26.04.2
    PRIMARY KEY (usn_id, ecosystem, source_package, fixed_version)
);
CREATE INDEX ubuntu_usn_fixes_package_idx ON ubuntu_usn_fixes (ecosystem, source_package);

CREATE TABLE ubuntu_usn_cves (
    usn_id    text NOT NULL REFERENCES ubuntu_usns (id) ON DELETE CASCADE,
    ecosystem text NOT NULL COLLATE "C",
    cve       text NOT NULL COLLATE "C",
    priority  text NOT NULL, -- prioridade do Ubuntu; vazia quando o aviso não informa
    cvss      text NOT NULL, -- vetor CVSS; vazio quando o aviso não informa
    PRIMARY KEY (usn_id, ecosystem, cve)
);
CREATE INDEX ubuntu_usn_cves_cve_idx ON ubuntu_usn_cves (cve);
