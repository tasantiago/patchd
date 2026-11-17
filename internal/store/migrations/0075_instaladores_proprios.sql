-- Instaladores próprios (Patch › Programas de terceiros › Incluir programa › instalador
-- próprio): o arquivo .msi ou .exe enviado pelo painel, sem winget. A origem fica na url como
-- "upload:<nome do arquivo>", e o winget_id fica vazio; o que vem do winget continua com o
-- endereço https e o pacote.
ALTER TABLE thirdparty_packages DROP CONSTRAINT thirdparty_packages_url_check;
ALTER TABLE thirdparty_packages DROP CONSTRAINT thirdparty_packages_winget_id_check;
ALTER TABLE thirdparty_packages ADD CONSTRAINT thirdparty_packages_origem_check CHECK (
    (url LIKE 'https://%' AND winget_id <> '') OR (url LIKE 'upload:%' AND winget_id = ''));
