-- 0028: aprovação de instaladores de terceiros (Aula 14.5). Um programa numa versão só é
-- instalado pelo patchd depois de aprovado; a aprovação guarda quem aprovou e quando.
--
-- A chave é o programa (a regra do manifesto) MAIS a versão: aprovar o 7-Zip 26.04 não
-- aprova o 26.05. A aprovação vale para os instaladores guardados dessa versão (o exe e o
-- MSI do 7-Zip, por exemplo): o binário de cada um já está preso pelo SHA-256 (Aula 14.2),
-- e a mesma versão com outro binário é recusada no registro.

CREATE TABLE thirdparty_approvals (
    app_id      text        NOT NULL CHECK (length(app_id) BETWEEN 1 AND 64),
    version     text        NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    approved_by text        NOT NULL CHECK (approved_by <> ''),
    approved_at timestamptz NOT NULL DEFAULT now(),
    note        text        NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    PRIMARY KEY (app_id, version)
);
