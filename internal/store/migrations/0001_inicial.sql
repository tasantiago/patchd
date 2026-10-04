-- 0001: máquinas e os relatórios recebidos (Aula 4.2).
-- Regra: uma migration aplicada nunca é editada. Mudanças entram em um arquivo novo.

-- Uma linha por máquina. O ID ainda vem da URL; na 4.3 passa a ser emitido pelo servidor.
CREATE TABLE machines (
    id                   text        PRIMARY KEY,
    first_seen_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    current_inventory_id bigint,
    current_scan_id      bigint
);

-- Um inventário por mudança de conteúdo: o reenvio com o mesmo hash não cria linha.
-- O relatório inteiro fica em JSONB; as colunas ao lado servem para listar e filtrar.
CREATE TABLE inventory_reports (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    machine_id     text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    hash           text        NOT NULL,
    schema_version integer     NOT NULL,
    agent_version  text        NOT NULL DEFAULT '',
    collected_at   timestamptz NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now(),
    hostname       text        NOT NULL DEFAULT '',
    os_family      text        NOT NULL DEFAULT '',
    os_name        text        NOT NULL DEFAULT '',
    os_version     text        NOT NULL DEFAULT '',
    report         jsonb       NOT NULL
);
CREATE INDEX inventory_reports_machine_idx ON inventory_reports (machine_id, received_at DESC);

-- Uma linha por busca recebida (a retenção entra junto com a agenda, na 4.4).
CREATE TABLE scan_reports (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    machine_id     text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    schema_version integer     NOT NULL,
    agent_version  text        NOT NULL DEFAULT '',
    scanned_at     timestamptz NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now(),
    reboot_pending boolean,    -- null = o agente não soube dizer
    report         jsonb       NOT NULL
);
CREATE INDEX scan_reports_machine_idx ON scan_reports (machine_id, received_at DESC);

-- Os ponteiros para o relatório atual de cada máquina.
ALTER TABLE machines
    ADD CONSTRAINT machines_current_inventory_fk FOREIGN KEY (current_inventory_id) REFERENCES inventory_reports (id),
    ADD CONSTRAINT machines_current_scan_fk      FOREIGN KEY (current_scan_id)      REFERENCES scan_reports (id);
