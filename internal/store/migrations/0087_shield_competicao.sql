-- 0087: grupos de competição do Shield (docs/shield.md, "Grupos de competição"). Um grupo é
-- uma disputa que a Central mostra em primeiro lugar, acima do ranking geral dos prédios:
-- "predios" põe prédios parecidos para disputar entre si; "andares" é um prédio só (com mais
-- de 3 andares) em que os andares disputam. Ligado ao ID estável do prédio (0085): a
-- renomeação do patrimônio não tira o prédio do grupo. Um prédio fica em um grupo só.
CREATE TABLE shield_competitions (
    id         bigint      PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    name       text        NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 80),
    kind       text        NOT NULL CHECK (kind IN ('predios', 'andares')),
    created_by text        NOT NULL CHECK (created_by <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_by text        NOT NULL CHECK (updated_by <> ''),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE shield_competition_buildings (
    building_id    bigint PRIMARY KEY REFERENCES buildings (id) ON DELETE CASCADE,
    competition_id bigint NOT NULL REFERENCES shield_competitions (id) ON DELETE CASCADE
);
CREATE INDEX shield_competition_buildings_group ON shield_competition_buildings (competition_id);
