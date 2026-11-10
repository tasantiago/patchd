-- 0070: tira do banco a senha da BIOS que veio pelo marcador do .bat (docs/bios-senha.md,
-- seção 11). O .bat gravava a senha em uso como a "versão" de um programa de mentira (a chave
-- Uninstall\BIOS_password), e o inventário de software a trouxe para cá como versão de
-- programa. O agente e o servidor deixaram de aceitar esse valor; aqui saem os já guardados.
-- As cópias do banco feitas antes desta migration ainda os têm: a troca da senha de cada
-- máquina pela guarda é o que os invalida.

UPDATE inventory_reports r
SET report = jsonb_set(r.report, '{software}', s.software)
FROM (
    SELECT r2.id,
           jsonb_agg(CASE WHEN lower(btrim(e.item->>'name')) = 'bios_password'
                            OR right(lower(e.item->>'source'), 14) = '\bios_password'
                          THEN e.item - 'version'
                          ELSE e.item END
                     ORDER BY e.ord) AS software
    FROM inventory_reports r2
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE WHEN jsonb_typeof(r2.report->'software') = 'array' THEN r2.report->'software' ELSE '[]'::jsonb END
    ) WITH ORDINALITY AS e(item, ord)
    WHERE position('bios_password' in lower(r2.report::text)) > 0
    GROUP BY r2.id
) s
WHERE r.id = s.id;

-- As respostas das perguntas ao parque: a pergunta "programa" de um agente anterior à correção
-- respondia com a senha ao lado do nome do marcador.
UPDATE jobs
SET detail = jsonb_build_object(
        'value', 'resposta retida',
        'extra', 'a resposta citava o marcador BIOS_password, que guarda a senha da BIOS; pergunte de novo com o agente atualizado')::text
WHERE type = 'pergunta'
  AND position('bios_password' in lower(detail)) > 0;
