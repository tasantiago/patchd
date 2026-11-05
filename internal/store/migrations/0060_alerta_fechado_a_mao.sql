-- 0060: alerta de desempenho fechado à mão (em lote, pela tela de Alertas): quem fechou e a
-- nota. Vazio: fechou sozinho, quando a medida voltou ao normal.

ALTER TABLE perf_alerts
    ADD COLUMN closed_by  text NOT NULL DEFAULT '' CHECK (length(closed_by) <= 200),
    ADD COLUMN close_note text NOT NULL DEFAULT '' CHECK (length(close_note) <= 500);
