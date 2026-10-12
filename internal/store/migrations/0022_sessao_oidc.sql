-- 0022: sessões abertas pelo RHBK (Aula 9.4).
--
-- A origem da sessão ganha "oidc": o login pelo RHBK (OpenID Connect), ao lado do usuário
-- local e do AD. Como no AD, o usuário do RHBK não tem linha em panel_users: o provedor
-- confere a senha e o claim de grupos diz o perfil a cada login.

ALTER TABLE panel_sessions DROP CONSTRAINT panel_sessions_source_check;
ALTER TABLE panel_sessions
    ADD CONSTRAINT panel_sessions_source_check CHECK (source IN ('local', 'ad', 'oidc'));
