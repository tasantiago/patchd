CREATE TABLE standardization_policies (
    id bigserial PRIMARY KEY,
    document jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE standardization_states (
    machine_id text NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    account text NOT NULL DEFAULT '',
    plan jsonb NOT NULL,
    shortcut_result jsonb,
    shortcuts_at timestamptz,
    evaluated_at timestamptz NOT NULL,
    PRIMARY KEY (machine_id, account)
);

CREATE INDEX standardization_states_recent ON standardization_states(evaluated_at DESC);

CREATE TABLE standardization_leases (
    machine_id text PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
    token text NOT NULL,
    expires_at timestamptz NOT NULL
);
