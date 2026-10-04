-- 0002: enrollment e credenciais das máquinas (Aula 4.3).

-- Tokens de enrollment criados pelo administrador. Só o hash do segredo é guardado.
CREATE TABLE enrollment_tokens (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash bytea       NOT NULL UNIQUE,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    max_uses   integer     NOT NULL CHECK (max_uses > 0),
    uses       integer     NOT NULL DEFAULT 0 CHECK (uses >= 0),
    revoked_at timestamptz
);

-- A máquina passa a ter credencial (só o hash), data e token do registro, e as evidências
-- de identidade que apresentou. Máquinas antigas (ID escolhido pelo cliente, Aula 4.1)
-- ficam sem credencial e não conseguem mais enviar nada.
ALTER TABLE machines
    ADD COLUMN credential_hash     bytea UNIQUE,
    ADD COLUMN enrolled_at         timestamptz,
    ADD COLUMN enrollment_token_id bigint REFERENCES enrollment_tokens (id),
    ADD COLUMN agent_version       text NOT NULL DEFAULT '',
    ADD COLUMN evidence            jsonb;
