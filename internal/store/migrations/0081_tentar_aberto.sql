-- "Tentar com o programa aberto" por programa de terceiros (docs/operacao.md): com o programa
-- aberto, o agente tenta instalar e só adia se não der certo. Sem linha: o da regra do manifesto.
CREATE TABLE thirdparty_try_open (
    app_id     text PRIMARY KEY,
    enabled    boolean NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
