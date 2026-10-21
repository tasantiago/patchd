-- 0041: o firmware em colunas do inventário (Aula 21.2), para a lista de Equipamentos filtrar
-- e contar sem ler o relatório (o mesmo motivo da 0025). Os booleanos ficam NULL quando o
-- agente não informou (agente anterior à 21.1, modelo sem a interface do fabricante): "não se
-- sabe" é diferente de "não".

ALTER TABLE inventory_reports
    ADD COLUMN fw_mode           text NOT NULL DEFAULT '',
    ADD COLUMN fw_secure_boot    boolean,
    ADD COLUMN fw_tpm            text NOT NULL DEFAULT '',
    ADD COLUMN fw_interface      text NOT NULL DEFAULT '',
    ADD COLUMN fw_admin_password boolean;

UPDATE inventory_reports i SET
    fw_mode           = coalesce(i.report->'hardware'->'firmware'->>'mode', ''),
    fw_secure_boot    = (i.report->'hardware'->'firmware'->>'secure_boot')::boolean,
    fw_tpm            = coalesce(i.report->'hardware'->'firmware'->>'tpm', ''),
    fw_interface      = coalesce(i.report->'hardware'->'firmware'->>'interface', ''),
    fw_admin_password = (i.report->'hardware'->'firmware'->>'admin_password')::boolean
FROM machines m
WHERE m.current_inventory_id = i.id AND jsonb_typeof(i.report->'hardware'->'firmware') = 'object';
