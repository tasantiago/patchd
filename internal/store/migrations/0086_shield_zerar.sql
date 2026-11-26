-- 0086: zerar a pontuação do Shield (docs/shield.md, "Zerar a pontuação"). Cada linha é uma
-- vez que o administrador zerou o XP, a sequência, o nível e as conquistas de todas as pessoas.
-- day: o último dia que fica para trás (no fuso da política); o fechamento não grava XP nem
-- sequência de um dia até ele, mesmo que um servidor refaça os dias na partida. A pontuação de
-- proteção das máquinas, os retratos do dia e os rankings por lugar não mudam.
CREATE TABLE shield_resets (
    id         bigint      PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    day        date        NOT NULL,
    reason     text        NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
    players    integer     NOT NULL,
    xp         bigint      NOT NULL,
    created_by text        NOT NULL CHECK (created_by <> ''),
    created_at timestamptz NOT NULL DEFAULT now()
);
