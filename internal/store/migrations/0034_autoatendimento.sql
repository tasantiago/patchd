-- 0034: autoatendimento (Aula 17.1). O que o admin libera para o usuário da máquina pedir
-- sozinho, por programa de terceiros (a regra do manifesto): atualizar o que já está
-- instalado e/ou instalar do zero, para quais grupos do AD (sAMAccountName ou DN; vazio:
-- qualquer usuário). Só entra versão aprovada e guardada: a oferta não aprova nada.

CREATE TABLE selfservice_offers (
    app_id     text        PRIMARY KEY,
    can_update boolean     NOT NULL,
    can_install boolean    NOT NULL,
    groups     text[]      NOT NULL DEFAULT '{}',
    updated_by text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (can_update OR can_install)
);
