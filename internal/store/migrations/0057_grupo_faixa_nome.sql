-- 0057: a página do grupo de implantação. ip_rings liga uma faixa de IPv4 (como o admin a
-- escreveu: 10.1.2.0/24, 10.1.2.10-50, 10.1.*) a um grupo, como building_rings faz com o
-- prédio; o prédio vence a faixa, e a mão vence os dois. ring_names dá um nome ao grupo
-- ("Piloto TI", "Fórum Cível"); o número continua sendo o que vale.

CREATE TABLE ip_rings (
    id         bigserial   PRIMARY KEY,
    spec       text        NOT NULL UNIQUE CHECK (length(spec) BETWEEN 1 AND 64),
    ring       smallint    NOT NULL CHECK (ring BETWEEN 0 AND 9),
    updated_by text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ring_names (
    ring       smallint    PRIMARY KEY CHECK (ring BETWEEN 0 AND 9),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    updated_by text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
