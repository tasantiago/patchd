-- 0083: os rankings do Shield por lugar (docs/shield.md): prédio e sala do patrimônio e o
-- andar (a faixa de IP, nos prédios com mais de uma). O avaliador grava o lugar de cada
-- máquina junto da pontuação e o ranking pronto, para qualquer servidor mostrar o mesmo.

ALTER TABLE shield_scores
    ADD COLUMN place_building text NOT NULL DEFAULT '',
    ADD COLUMN place_room     text NOT NULL DEFAULT '',
    ADD COLUMN place_floor    text NOT NULL DEFAULT '';

-- kind: predio, andar ou sala. key: o prédio, ou o prédio e a faixa/sala. label: o nome que
-- as pessoas veem (o andar com o nome da política, nunca a faixa de IP).
CREATE TABLE shield_rankings (
    kind     text    NOT NULL CHECK (kind IN ('predio', 'andar', 'sala')),
    key      text    NOT NULL,
    building text    NOT NULL,
    name     text    NOT NULL DEFAULT '',
    label    text    NOT NULL,
    machines integer NOT NULL,
    sealed   integer NOT NULL,
    average  numeric(5,2) NOT NULL,
    pos      integer NOT NULL,
    overall  integer NOT NULL,
    of_total integer NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE INDEX shield_rankings_building ON shield_rankings(kind, building);
