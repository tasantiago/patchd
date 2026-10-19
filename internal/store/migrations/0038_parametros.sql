-- 0038: parâmetros mudados pela tela (Aula 19.2). Cada linha troca o valor de uma variável do
-- .env enquanto existir; "restaurar o padrão" apaga a linha e o .env volta a valer. Só as
-- variáveis que o servidor deixa mudar pela tela entram (a lista fica no código); banco,
-- portas, TLS, chaves e segredos ficam sempre no ambiente.

CREATE TABLE settings (
    key        text        PRIMARY KEY CHECK (key ~ '^PATCHD_[A-Z0-9_]{1,60}$'),
    value      text        NOT NULL CHECK (length(value) <= 2000),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text        NOT NULL DEFAULT ''
);
