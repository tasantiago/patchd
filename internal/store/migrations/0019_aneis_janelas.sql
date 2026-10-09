-- 0019: anéis e janelas de manutenção (Aula 8.3, RF-25).
--
-- Anel: 0 é o piloto (recebe primeiro), 1 é o padrão de toda máquina, até 9. A janela é
-- por anel, no horário local de um fuso: dias da semana (bit 0 = segunda ... bit 6 =
-- domingo), início em minutos desde a meia-noite e duração. Um job criado "na janela"
-- guarda o início dela em not_before: o servidor só entrega a partir daí, e o agente,
-- que lê o not_before do payload assinado, também confere.

ALTER TABLE machines
    ADD COLUMN ring smallint NOT NULL DEFAULT 1 CHECK (ring BETWEEN 0 AND 9);

CREATE TABLE maintenance_windows (
    ring         smallint    PRIMARY KEY CHECK (ring BETWEEN 0 AND 9),
    days         smallint    NOT NULL CHECK (days BETWEEN 1 AND 127),
    start_min    smallint    NOT NULL CHECK (start_min BETWEEN 0 AND 1439),
    duration_min smallint    NOT NULL CHECK (duration_min BETWEEN 10 AND 1440),
    timezone     text        NOT NULL,
    updated_by   text        NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE jobs ADD COLUMN not_before timestamptz;
