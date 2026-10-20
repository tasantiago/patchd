-- 0040: registro dos ensaios de restauração (Aula 20.8, F4). O deploy/restaurar.sh --testar
-- grava uma linha no fim de cada ensaio: ok quando as contagens restauradas conferem com as
-- do momento da cópia. Configuração › Sistema mostra a cópia e o ensaio mais recentes: uma
-- cópia que nunca foi restaurada não é uma cópia.

CREATE TABLE restore_tests (
    id          bigserial   PRIMARY KEY,
    finished_at timestamptz NOT NULL DEFAULT now(),
    file        text        NOT NULL CHECK (file ~ '^patchd-[0-9]{8}T[0-9]{6}Z\.dump(\.age)?$'),
    ok          boolean     NOT NULL
);

CREATE INDEX restore_tests_finished_at ON restore_tests (finished_at DESC);
