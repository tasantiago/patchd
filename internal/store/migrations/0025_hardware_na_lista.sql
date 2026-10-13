-- 0025: fabricante, modelo, serial e tipo de chassi em colunas do inventário (Aula 12.2),
-- para a lista de Equipamentos.
--
-- Os mesmos valores já estão no relatório (report->'hardware'), mas tirar um campo de um
-- jsonb obriga o PostgreSQL a ler o documento inteiro: com o software junto, são centenas
-- de KB por máquina, e a lista leria a frota toda a cada página. É o motivo de hostname e
-- os_name serem colunas desde a 0001.
--
-- ADD COLUMN com DEFAULT constante não reescreve a tabela (PostgreSQL 11+). O UPDATE
-- preenche só o inventário atual de cada máquina: os antigos ficam com '' (a lista não os
-- usa, e a retenção os apaga), e a migration não relê o histórico inteiro.

ALTER TABLE inventory_reports
    ADD COLUMN hw_manufacturer text NOT NULL DEFAULT '',
    ADD COLUMN hw_model        text NOT NULL DEFAULT '',
    ADD COLUMN hw_serial       text NOT NULL DEFAULT '',
    ADD COLUMN hw_chassis      text NOT NULL DEFAULT '';

UPDATE inventory_reports i SET
    hw_manufacturer = coalesce(i.report->'hardware'->>'manufacturer', ''),
    hw_model        = coalesce(i.report->'hardware'->>'model', ''),
    hw_serial       = coalesce(i.report->'hardware'->>'serial_number', ''),
    hw_chassis      = coalesce(i.report->'hardware'->>'chassis_type', '')
FROM machines m
WHERE m.current_inventory_id = i.id;
