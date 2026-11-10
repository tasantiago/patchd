-- 0067: campanhas arquivadas. A encerrada (concluída ou cancelada) pode sair da lista sem
-- perder nada; a arquivada pode ser excluída, e os jobs dela ficam no histórico sem a
-- ligação com a campanha.

ALTER TABLE campaigns ADD COLUMN archived_at timestamptz;
