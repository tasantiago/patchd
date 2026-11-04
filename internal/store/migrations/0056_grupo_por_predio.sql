-- 0056: o grupo de implantação pelo prédio do patrimônio. building_rings liga um prédio (o
-- DEPREDIO do CSV, exato) a um grupo; as máquinas do prédio vão para ele, menos as movidas à
-- mão (ring_manual), que ficam como exceção até alguém devolvê-las ao vínculo.
--
-- Quem já estava fora do grupo padrão foi posto lá por alguém: entra como movida à mão, para
-- que o primeiro vínculo não tire um piloto do grupo 0.

ALTER TABLE machines ADD COLUMN ring_manual boolean NOT NULL DEFAULT false;
UPDATE machines SET ring_manual = true WHERE ring <> 1;

CREATE TABLE building_rings (
    building   text        PRIMARY KEY CHECK (length(building) BETWEEN 1 AND 200),
    ring       smallint    NOT NULL CHECK (ring BETWEEN 0 AND 9),
    updated_by text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
