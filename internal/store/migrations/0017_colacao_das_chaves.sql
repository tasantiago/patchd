-- 0017: a colação das chaves estrangeiras igual à da chave que elas referenciam (Aula 7.8).
--
-- As chaves dos catálogos foram criadas com COLLATE "C" (ordem byte a byte, como o Go),
-- mas as colunas que as referenciam ficaram com a colação padrão do banco. Para o
-- PostgreSQL, comparar duas colunas de colações diferentes usa a colação explícita ("C"),
-- e um índice só serve a uma comparação na mesma colação. Resultado: a chave primária de
-- ubuntu_usn_cves (usn_id, ...) não era usada nas subconsultas da avaliação do Ubuntu, que
-- liam a tabela inteira (246 mil linhas) para cada aviso: 4 s por máquina, medidos com
-- compliance -tempos (Aula 7.7). Com a colação igual, o índice volta a servir.
--
-- O ALTER reescreve as tabelas e os índices; com o tamanho do catálogo, leva menos de 1 s.

ALTER TABLE ubuntu_usn_fixes     ALTER COLUMN usn_id          TYPE text COLLATE "C";
ALTER TABLE ubuntu_usn_cves      ALTER COLUMN usn_id          TYPE text COLLATE "C";
ALTER TABLE msrc_vulns           ALTER COLUMN document_id     TYPE text COLLATE "C";
ALTER TABLE msrc_affected        ALTER COLUMN document_id     TYPE text COLLATE "C"; -- referenciam msrc_vulns
ALTER TABLE msrc_fixes           ALTER COLUMN document_id     TYPE text COLLATE "C";
ALTER TABLE fedora_update_builds ALTER COLUMN alias           TYPE text COLLATE "C";
ALTER TABLE fedora_update_cves   ALTER COLUMN alias           TYPE text COLLATE "C";
ALTER TABLE apple_release_cves   ALTER COLUMN product_version TYPE text COLLATE "C";

-- Estatísticas novas para o planejador escolher os índices já na primeira consulta.
ANALYZE ubuntu_usn_fixes, ubuntu_usn_cves, msrc_vulns, msrc_affected, msrc_fixes, fedora_update_builds, fedora_update_cves, apple_release_cves;
