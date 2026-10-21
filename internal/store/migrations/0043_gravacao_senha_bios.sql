-- 0043: a gravação da senha da BIOS (Aula 21.5). A senha pendente ganha o job que a leva à
-- máquina (job_id) e o que a máquina respondeu (result, sem senha nenhuma). Sem chave
-- estrangeira para jobs: a retenção apaga jobs antigos, e a guarda fica.
--
-- No máximo uma senha ativa por máquina: a ativação marca a anterior como substituída na
-- mesma transação.

ALTER TABLE bios_passwords
    ADD COLUMN job_id bigint,
    ADD COLUMN result text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX bios_passwords_uma_ativa ON bios_passwords (machine_id) WHERE state = 'ativa';
