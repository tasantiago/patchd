-- 0058: campanha em horário fixo todo dia (ex.: 02:00 por 3 h), sem depender da janela de
-- cada grupo de implantação. Os três campos andam juntos: todos nulos (com prazo ou na janela
-- do grupo) ou todos preenchidos.

ALTER TABLE campaigns
    ADD COLUMN daily_start_min    integer CHECK (daily_start_min BETWEEN 0 AND 1439),
    ADD COLUMN daily_duration_min integer CHECK (daily_duration_min BETWEEN 10 AND 1440),
    ADD COLUMN daily_tz           text    CHECK (length(daily_tz) BETWEEN 1 AND 64),
    ADD CONSTRAINT campaigns_daily_juntos CHECK (
        (daily_start_min IS NULL) = (daily_duration_min IS NULL) AND (daily_start_min IS NULL) = (daily_tz IS NULL));
