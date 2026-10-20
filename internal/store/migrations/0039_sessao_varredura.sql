-- 0039: sessões para varredura de segurança (Aula 20.4).
--
-- A origem da sessão ganha "varredura": uma sessão que o operador abre pela linha de comando
-- (patchd-server session create) para uma ferramenta como o ZAP percorrer o painel, sem
-- precisar de uma conta do AD só para isso. Prazo curto, perfil escolhido, trilha na
-- auditoria (cli.session.create) e fim com "session end".

ALTER TABLE panel_sessions DROP CONSTRAINT panel_sessions_source_check;
ALTER TABLE panel_sessions
    ADD CONSTRAINT panel_sessions_source_check CHECK (source IN ('local', 'ad', 'oidc', 'varredura'));
