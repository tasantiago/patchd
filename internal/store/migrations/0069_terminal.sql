-- 0069: o terminal do administrador (docs/terminal.md). A sessão guarda quem abriu, em qual
-- máquina e o motivo (obrigatório); cada comando guarda o texto exato, quem mandou, de onde,
-- o job assinado que o levou e, na volta, o código de saída e o fim da saída. O registro não
-- é editado nem apagado pelo painel; sai sozinho depois de 365 dias.

CREATE TABLE terminal_sessions (
    id         bigserial   PRIMARY KEY,
    machine_id text        NOT NULL REFERENCES machines (id),
    username   text        NOT NULL,
    reason     text        NOT NULL CHECK (length(reason) BETWEEN 10 AND 400),
    remote     text        NOT NULL DEFAULT '',
    opened_at  timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX terminal_sessions_machine ON terminal_sessions (machine_id, username, expires_at DESC);

CREATE TABLE terminal_commands (
    id          bigserial   PRIMARY KEY,
    session_id  bigint      NOT NULL REFERENCES terminal_sessions (id),
    machine_id  text        NOT NULL REFERENCES machines (id),
    username    text        NOT NULL,
    remote      text        NOT NULL DEFAULT '',
    shell       text        NOT NULL CHECK (shell IN ('powershell', 'cmd', 'bash')),
    command     text        NOT NULL,
    timeout_sec integer     NOT NULL,
    job_id      bigint      REFERENCES jobs (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL,
    finished_at timestamptz,
    exit_code   integer,
    output      text        NOT NULL DEFAULT '',
    truncated   boolean     NOT NULL DEFAULT false,
    note        text        NOT NULL DEFAULT ''
);
CREATE INDEX terminal_commands_machine ON terminal_commands (machine_id, id DESC);
CREATE INDEX terminal_commands_user ON terminal_commands (username, id DESC);
CREATE INDEX terminal_commands_job ON terminal_commands (job_id) WHERE job_id IS NOT NULL;
CREATE INDEX terminal_commands_created ON terminal_commands (created_at);
