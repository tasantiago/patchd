-- 0045: descoberta de rede (Aula 23.7). O admin cadastra faixas de IP em Descoberta; o
-- servidor varre (sondagem TCP) e guarda os hosts vivos. Cruzando com o inventário (pelo IP
-- e pelo nome), o painel mostra os que estão SEM AGENTE, com o comando de instalação pronto.
--
-- A cada varredura os hosts são regravados por inteiro (o resultado é uma foto da rede, não
-- um acúmulo). As faixas e o histórico de varreduras ficam.

CREATE TABLE discovery_ranges (
    id         bigserial   PRIMARY KEY,
    spec       text        NOT NULL UNIQUE CHECK (spec <> '' AND length(spec) <= 64),
    note       text        NOT NULL DEFAULT '' CHECK (length(note) <= 120),
    enabled    boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text        NOT NULL DEFAULT ''
);

-- Os hosts vivos da última varredura. match_id/match_kind preenchidos quando o host casou
-- com uma máquina do inventário (match_kind: 'ip' ou 'nome'); nulos = sem agente.
CREATE TABLE discovery_hosts (
    ip            inet        PRIMARY KEY,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    ports         text        NOT NULL DEFAULT '' CHECK (length(ports) <= 120),
    hostname      text        NOT NULL DEFAULT '' CHECK (length(hostname) <= 255),
    os_guess      text        NOT NULL DEFAULT '' CHECK (os_guess IN ('', 'windows', 'ssh')),
    match_id      text        CHECK (match_id IS NULL OR length(match_id) <= 64),
    match_kind    text        NOT NULL DEFAULT '' CHECK (match_kind IN ('', 'ip', 'nome'))
);

-- O histórico das varreduras: quem, quando, quantos endereços e quantos sem agente.
CREATE TABLE discovery_runs (
    id            bigserial   PRIMARY KEY,
    started_at    timestamptz NOT NULL,
    finished_at   timestamptz,
    scanned       integer     NOT NULL DEFAULT 0 CHECK (scanned >= 0),
    alive         integer     NOT NULL DEFAULT 0 CHECK (alive >= 0),
    without_agent integer     NOT NULL DEFAULT 0 CHECK (without_agent >= 0),
    partial       boolean     NOT NULL DEFAULT false,
    started_by    text        NOT NULL DEFAULT '',
    note          text        NOT NULL DEFAULT '' CHECK (length(note) <= 200)
);
