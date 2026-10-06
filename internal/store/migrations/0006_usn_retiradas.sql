-- 0006: a USN retirada pelo Ubuntu fica registrada (Aula 6.2).
--
-- O modified_id.csv do OSV continua listando os registros retirados (withdrawn). Na 0005,
-- a USN retirada era apagada; sem linha guardada, a sincronização seguinte a via como
-- "nova" e a baixava de novo, em toda execução. Agora a linha fica, com a data da
-- retirada e sem correções nem CVEs, e as consultas ignoram as retiradas.
-- (A 0005 já estava aplicada no laboratório: migration aplicada nunca é editada.)
ALTER TABLE ubuntu_usns ADD COLUMN withdrawn timestamptz; -- null: o aviso vale
