-- 0077: a categoria do programa na loja da Central de software (protocol.StoreCategories).
-- Vazia: a categoria padrão do programa (thirdparty.DefaultStoreCategory).

ALTER TABLE selfservice_offers ADD COLUMN category text NOT NULL DEFAULT '';
