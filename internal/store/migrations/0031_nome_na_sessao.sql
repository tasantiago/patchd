-- 0031: nome de exibição na sessão do painel (Aula 15.5).
--
-- O AD (displayName) e o RHBK (claim "name") dizem o nome da pessoa no login; ele fica na
-- sessão para o "Olá" do topo e para a lista de sessões abertas. Como o perfil, é o do
-- momento do login. A conta local não tem nome: fica vazio e o painel mostra o usuário.
ALTER TABLE panel_sessions ADD COLUMN display_name text NOT NULL DEFAULT '';
