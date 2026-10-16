-- 0033: campanha de reinício por anel (Aula 16.5). reboot_delay_sec: o aviso aos usuários
-- antes do reinício, o mesmo do job reboot (de 2 a 60 minutos); 0 nos outros tipos.

ALTER TABLE campaigns
    ADD COLUMN reboot_delay_sec integer NOT NULL DEFAULT 0;
