-- 0029: campanha de programa de terceiros (Aula 14.7). app e version: o programa e a versão
-- aprovada que a campanha leva por todos os anéis (vazios nos outros tipos). notes: um
-- resumo de cada anel liberado, com os jobs criados e as máquinas que ficaram fora, por
-- motivo ("anel 0: 1 job(s); fora: 1 em dia, 3 sem o programa").

ALTER TABLE campaigns
    ADD COLUMN app     text   NOT NULL DEFAULT '',
    ADD COLUMN version text   NOT NULL DEFAULT '',
    ADD COLUMN notes   text[] NOT NULL DEFAULT '{}';
