-- 0050: a senha padrão da BIOS de cada fabricante (docs/bios-senha.md, seção 10): a que as
-- máquinas têm antes da primeira troca pela guarda. Como a guarda, só o envelope fechado para
-- a chave de guarda; uma linha por fabricante.

CREATE TABLE bios_fleet_passwords (
    vendor        text        PRIMARY KEY CHECK (vendor IN ('hp', 'dell', 'lenovo')),
    escrow        bytea       NOT NULL,
    escrow_key_id text        NOT NULL CHECK (escrow_key_id ~ '^[0-9a-f]{12}$'),
    set_at        timestamptz NOT NULL,
    set_by        text        NOT NULL
);
