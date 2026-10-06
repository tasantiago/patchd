-- 0009: catálogo KEV da CISA, as CVEs comprovadamente exploradas (Aula 6.3).
--
-- O feed inteiro tem 1,8 MB: quando o dateReleased muda (guardado em catalog_feeds), a
-- tabela é substituída numa transação. A priorização é uma consulta (CVE do catálogo que
-- está no KEV ou que o fabricante marca como explorada); nada é copiado para as tabelas
-- das outras fontes, que têm o seu próprio ritmo de sincronização.

CREATE TABLE kev_vulns (
    cve        text    PRIMARY KEY COLLATE "C",
    vendor     text    NOT NULL,
    product    text    NOT NULL,
    name       text    NOT NULL,
    date_added date    NOT NULL,
    due_date   date,              -- prazo da CISA para as agências federais americanas
    ransomware boolean NOT NULL,  -- uso conhecido em campanhas de ransomware
    cwes       text[]  NOT NULL
);
CREATE INDEX kev_vulns_added_idx ON kev_vulns (date_added);
