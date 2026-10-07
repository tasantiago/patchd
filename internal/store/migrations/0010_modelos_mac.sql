-- 0010: modelos de Mac e as majors do macOS que cada um aceita (Aula 6.4, parte 4).
--
-- Vêm da seção Models do feed do SOFA, que a Aula 6.2 descartava. A chave é o identificador
-- do modelo ("Mac14,14"), o mesmo que o agente lê do system_profiler; o gdmf usa o board ID,
-- que o agente não coleta.

CREATE TABLE apple_models (
    model          text      PRIMARY KEY COLLATE "C", -- ex.: Mac14,14
    marketing_name text      NOT NULL,                -- ex.: Mac Studio (M2 Ultra, 2023)
    majors         integer[] NOT NULL                 -- da mais nova para a mais antiga: {27,26,15,14,13}
);

-- O feed guardado não tinha os modelos e o UpdateHash dele não muda por causa desta
-- migração: sem apagar o hash, a próxima sincronização diria "em dia" e a tabela ficaria
-- vazia. Apagando, o SOFA é baixado e gravado de novo, agora com os modelos.
DELETE FROM catalog_feeds WHERE source = 'apple-sofa';
