-- Programas de terceiros incluídos pelo painel (Patch › Programas de terceiros › Incluir
-- programa): a regra, no formato do manifesto (internal/thirdparty), somada ao embutido e ao
-- local. O id é o da regra; o SO é sempre windows.
CREATE TABLE thirdparty_custom_apps (
    id         text PRIMARY KEY,
    rule       jsonb NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
