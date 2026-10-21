-- 0042: a guarda da senha de administrador da BIOS (Aula 21.4; desenho em docs/bios-senha.md).
-- Cada linha é uma senha sorteada para uma máquina, fechada para a chave pública da guarda
-- (escrow): o banco e as cópias dele não leem nenhuma senha. A senha nasce "pendente" (guardada,
-- ainda não gravada na BIOS); a gravação (21.5) a leva para "ativa", "falhou" ou "incerta", e
-- a anterior para "substituida".
--
-- Fora da retenção de propósito: a senha continua na BIOS enquanto a placa existir, inclusive
-- de máquina aposentada (o equipamento pode voltar). Sem ON DELETE: apagar uma máquina com
-- senha guardada é um erro, não uma limpeza.

CREATE TABLE bios_passwords (
    id               bigserial   PRIMARY KEY,
    machine_id       text        NOT NULL REFERENCES machines (id),
    state            text        NOT NULL CHECK (state IN ('pendente', 'ativa', 'incerta', 'falhou', 'substituida')),
    escrow           bytea       NOT NULL,
    escrow_key_id    text        NOT NULL CHECK (escrow_key_id ~ '^[0-9a-f]{12}$'),
    created_at       timestamptz NOT NULL,
    created_by       text        NOT NULL,
    updated_at       timestamptz NOT NULL,
    reveals          integer     NOT NULL DEFAULT 0,
    last_revealed_at timestamptz,
    last_revealed_by text        NOT NULL DEFAULT ''
);

CREATE INDEX bios_passwords_machine ON bios_passwords (machine_id, id DESC);
-- No máximo uma senha esperando a gravação por máquina.
CREATE UNIQUE INDEX bios_passwords_uma_pendente ON bios_passwords (machine_id) WHERE state = 'pendente';
