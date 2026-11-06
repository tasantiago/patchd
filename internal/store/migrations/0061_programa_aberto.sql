-- 0061: programa aberto no job de terceiros.
--
-- jobs.unblocked_at: o agente avisou que o programa que adiou o job fechou ("tentar assim
-- que fechar"); a campanha pede de novo na hora, sem esperar o intervalo.
-- machines.capabilities: o que o agente anunciou no último check-in (fechar o programa com
-- aviso só vai para quem anuncia).
-- campaigns.close_delay_sec: a campanha de terceiros fecha o programa aberto, com aviso de
-- tantos segundos (0: não fecha, o padrão).

ALTER TABLE jobs ADD COLUMN unblocked_at timestamptz;
ALTER TABLE machines ADD COLUMN capabilities text[] NOT NULL DEFAULT '{}';
ALTER TABLE campaigns
    ADD COLUMN close_delay_sec integer NOT NULL DEFAULT 0 CHECK (close_delay_sec BETWEEN 0 AND 7200);
