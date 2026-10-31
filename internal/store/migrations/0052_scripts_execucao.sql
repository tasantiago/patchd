-- Configuração reutilizável de execução. Não altera os jobs já assinados.
-- Cadastros antigos continuam sem entrada padrão e com timeout de 10 minutos.
ALTER TABLE script_bundles
    ADD COLUMN entry text NOT NULL DEFAULT '',
    ADD COLUMN args jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(args) = 'array'),
    ADD COLUMN timeout_s integer NOT NULL DEFAULT 600 CHECK (timeout_s BETWEEN 30 AND 3600);
