-- 0015: máquinas aposentadas (Aula 7.5).
--
-- Aposentar tira a máquina da frota (lista, compliance, painel) e faz a credencial dela
-- deixar de valer, sem apagar nada: os relatórios e os alertas de identidade ficam.
-- É reversível (restore). O caso típico é o registro antigo de uma VM reinstalada.

ALTER TABLE machines
    ADD COLUMN retired_at     timestamptz,
    ADD COLUMN retired_reason text;
