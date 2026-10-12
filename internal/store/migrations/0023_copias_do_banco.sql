-- 0023: registro das cópias do banco (Aula 10.3). O deploy/backup.sh grava uma linha no fim
-- de cada cópia conferida; o servidor publica a mais recente nas métricas, e o alerta
-- PatchdSemCopiaRecente avisa quando o timer parou de rodar sem ninguém perceber.
--
-- Só o nome do arquivo, sem a pasta (a pasta é do host, e o banco vai nas cópias).

CREATE TABLE backup_runs (
    id          bigserial   PRIMARY KEY,
    finished_at timestamptz NOT NULL DEFAULT now(),
    file        text        NOT NULL CHECK (file ~ '^patchd-[0-9]{8}T[0-9]{6}Z\.dump(\.age)?$'),
    bytes       bigint      NOT NULL CHECK (bytes > 0),
    encrypted   boolean     NOT NULL
);

CREATE INDEX backup_runs_finished_at ON backup_runs (finished_at DESC);
