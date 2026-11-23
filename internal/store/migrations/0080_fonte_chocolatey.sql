-- A fonte do instalador pelo Chocolatey (o repositório da comunidade): o id do pacote e,
-- opcional, as variáveis do script com o link e o SHA-256 (o Adobe usa $MUIurl64).
ALTER TABLE thirdparty_sources DROP CONSTRAINT IF EXISTS thirdparty_sources_kind_check;
ALTER TABLE thirdparty_sources ADD CONSTRAINT thirdparty_sources_kind_check CHECK (kind IN ('winget', 'url', 'chocolatey'));
ALTER TABLE thirdparty_sources ADD COLUMN package_id text NOT NULL DEFAULT '';
ALTER TABLE thirdparty_sources ADD COLUMN url_var text NOT NULL DEFAULT '';
ALTER TABLE thirdparty_sources ADD COLUMN checksum_var text NOT NULL DEFAULT '';
