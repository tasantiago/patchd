-- 0003: evidências normalizadas e alertas de identidade (Aula 4.3, parte 2).

-- Chaves comparáveis, calculadas pelo servidor no registro. A evidência crua continua em
-- "evidence". Máquinas registradas antes desta migration ficam com as chaves nulas.
ALTER TABLE machines
    ADD COLUMN install_id   text,
    ADD COLUMN hardware_key text,
    ADD COLUMN serial       text;
CREATE INDEX machines_install_id_idx   ON machines (install_id)   WHERE install_id   IS NOT NULL;
CREATE INDEX machines_hardware_key_idx ON machines (hardware_key) WHERE hardware_key IS NOT NULL;
CREATE INDEX machines_serial_idx       ON machines (serial)       WHERE serial       IS NOT NULL;

-- Alertas: uma máquina nova relacionada a uma já conhecida. Nunca uma fusão.
CREATE TABLE identity_links (
    id                 bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    machine_id         text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    related_machine_id text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    relation           text        NOT NULL CHECK (relation IN ('reenrollment', 'clone', 'same_hardware')),
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (machine_id, related_machine_id)
);
