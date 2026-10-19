-- 0037: aparência do painel (Aula 19.1). O logotipo enviado pelo admin fica no banco, e não numa
-- pasta: dois servidores no mesmo banco mostram o mesmo, e a cópia do banco leva junto. Só PNG
-- ou JPEG, até 256 KB; o servidor confere os bytes antes de gravar e serve com o tipo daqui.

CREATE TABLE panel_assets (
    name         text        PRIMARY KEY CHECK (name ~ '^[a-z]{1,20}$'),
    content_type text        NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg')),
    data         bytea       NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 262144),
    sha256       text        NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text        NOT NULL DEFAULT ''
);
