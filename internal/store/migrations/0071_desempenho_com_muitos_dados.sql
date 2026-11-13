-- Gargalos medidos com um parque de 4.000 máquinas, 400 mil jobs e 4,5 milhões de amostras.

-- Os jobs de uma máquina (a aba Tarefas, a padronização a cada pedido do agente, a
-- mesclagem): só havia índice para os jobs abertos, e a lista varria a tabela inteira.
CREATE INDEX jobs_machine_idx ON jobs (machine_id, id DESC);

-- "Quantos jobs terminaram desde ontem" (Início e Visão geral): contava varrendo tudo.
CREATE INDEX jobs_finished_idx ON jobs (finished_at) WHERE finished_at IS NOT NULL;

-- Horas do desempenho que receberam amostra atrasada (o notebook que ficou fora da rede e
-- manda a fila ao voltar). A passada de hora em hora refazia as últimas 48 horas inteiras,
-- de todas as máquinas, para não perder essas; agora refaz só as horas recentes e as que
-- estão aqui.
CREATE TABLE perf_late (
    machine_id text        NOT NULL REFERENCES machines (id) ON DELETE CASCADE,
    hour       timestamptz NOT NULL,
    PRIMARY KEY (machine_id, hour)
);

-- O firmware numa coluna só dele (o objeto hardware.firmware do relatório, com as
-- configurações da BIOS). As telas de BIOS em lote liam o relatório inteiro de cada máquina,
-- com a lista de programas junto, para tirar dele um booleano ou as configurações: ~40 KB
-- descomprimidos por máquina. NULL: o agente não informou o firmware, ou o relatório é do
-- histórico (as telas só olham o atual de cada máquina, e só ele é preenchido aqui).
ALTER TABLE inventory_reports ADD COLUMN fw_detail jsonb;

UPDATE inventory_reports i SET fw_detail = i.report #> '{hardware,firmware}'
FROM machines m
WHERE m.current_inventory_id = i.id AND jsonb_typeof(i.report #> '{hardware,firmware}') = 'object';

-- Os pedidos do autoatendimento (Automação › Autoatendimento) são uma fração pequena dos
-- jobs, e a lista varria os jobs de 30 dias do parque para achá-los pelo autor.
CREATE INDEX jobs_selfservice_idx ON jobs (id DESC) WHERE starts_with(created_by, 'autoatendimento: ');

-- O último job de configuração da BIOS de cada máquina (BIOS em lote e padrão da BIOS).
CREATE INDEX jobs_bios_settings_idx ON jobs (machine_id, id DESC) WHERE type = 'bios_settings';

-- "Jobs dos últimos 7 dias, por tipo e estado" (Início e Visão geral): com o tipo e o estado
-- no índice, a contagem não visita a tabela. Substitui o índice só da data.
CREATE INDEX jobs_created_type_idx ON jobs (created_at, type, state);
DROP INDEX jobs_created_idx;
