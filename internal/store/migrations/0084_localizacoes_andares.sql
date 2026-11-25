-- Cadastro físico independente do Shield; a mesma faixa pode ter nomes distintos por prédio.
CREATE TABLE floor_locations (
    building text NOT NULL,
    spec text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL,
    PRIMARY KEY (building, spec)
);
