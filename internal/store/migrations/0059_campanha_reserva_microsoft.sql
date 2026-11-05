-- 0059: campanha do Windows com a fonte reserva. Se a fonte da política falhar ou não
-- oferecer o KB, o job vai com a fonte Microsoft (Microsoft Update direto), desde que a
-- reserva esteja ligada em Parâmetros e o agente a entenda. Padrão: desligada.

ALTER TABLE campaigns
    ADD COLUMN microsoft_fallback boolean NOT NULL DEFAULT false;
