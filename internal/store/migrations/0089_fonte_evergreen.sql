-- 0089: a fonte do instalador pelo Evergreen (docs/operacao.md, "Fonte do instalador"): o
-- servidor baixa o link (URI) que o Evergreen informa para a versão, com o SHA-256 quando ele
-- informa. package_id é o aplicativo no Evergreen; filter, as linhas que contam
-- ("Architecture=x64; Channel=Stable").
ALTER TABLE thirdparty_sources DROP CONSTRAINT IF EXISTS thirdparty_sources_kind_check;
ALTER TABLE thirdparty_sources ADD CONSTRAINT thirdparty_sources_kind_check CHECK (kind IN ('winget', 'url', 'chocolatey', 'evergreen'));
ALTER TABLE thirdparty_sources ADD COLUMN filter text NOT NULL DEFAULT '' CHECK (length(filter) <= 300);
