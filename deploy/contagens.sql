-- Contagens para conferir uma cópia do banco (Aula 10.1): versão do esquema e o tamanho
-- das tabelas que só existem aqui. O catálogo (MSRC, USN, Fedora, Apple, KEV) fica de fora:
-- ele se refaz sozinho na próxima sincronização.
SELECT 'versao_do_banco', max(version) FROM schema_migrations
UNION ALL SELECT 'maquinas_ativas', count(*) FROM machines WHERE retired_at IS NULL
UNION ALL SELECT 'maquinas_aposentadas', count(*) FROM machines WHERE retired_at IS NOT NULL
UNION ALL SELECT 'inventarios', count(*) FROM inventory_reports
UNION ALL SELECT 'buscas', count(*) FROM scan_reports
UNION ALL SELECT 'jobs', count(*) FROM jobs
UNION ALL SELECT 'campanhas', count(*) FROM campaigns
UNION ALL SELECT 'janelas', count(*) FROM maintenance_windows
UNION ALL SELECT 'tokens_de_registro', count(*) FROM enrollment_tokens
UNION ALL SELECT 'usuarios_locais', count(*) FROM panel_users
ORDER BY 1;
