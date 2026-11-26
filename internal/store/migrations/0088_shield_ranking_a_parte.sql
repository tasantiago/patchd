-- 0088: grupo de competição com ranking geral à parte (docs/shield.md, "Grupos de
-- competição"). separate = true: os prédios do grupo saem do ranking de todos os prédios e o
-- ranking do grupo é o geral deles (os prédios pequenos das cidades, como os fóruns digitais,
-- disputam entre si e não com as sedes). Só no grupo de prédios.
ALTER TABLE shield_competitions ADD COLUMN separate boolean NOT NULL DEFAULT false;
ALTER TABLE shield_competitions ADD CONSTRAINT shield_competitions_separate_predios CHECK (NOT separate OR kind = 'predios');
