-- 0004: catálogo de segurança da Microsoft (Aula 6.1).
--
-- Um documento do MSRC é a unidade de sincronização: quando a lista do MSRC traz uma data
-- de revisão diferente, o documento inteiro é apagado e gravado de novo, numa transação.
-- Por isso as CVEs, as ligações e as correções pertencem ao documento (a mesma CVE pode
-- aparecer em mais de um documento, quando é revisada num mês seguinte).

CREATE TABLE msrc_documents (
    id               text        PRIMARY KEY COLLATE "C", -- ex.: 2026-Sep
    title            text        NOT NULL,
    initial_release  timestamptz NOT NULL,
    current_release  timestamptz NOT NULL,                -- a data da lista; é o que a sincronização compara
    fetched_at       timestamptz NOT NULL DEFAULT now(),
    skipped_fixes    integer     NOT NULL                 -- correções do tipo 2 sem número de KB
);

-- O ProductID do MSRC é estável entre os documentos; o nome mais recente vale.
CREATE TABLE msrc_products (
    id   text PRIMARY KEY COLLATE "C",
    name text NOT NULL
);

CREATE TABLE msrc_vulns (
    document_id text    NOT NULL REFERENCES msrc_documents (id) ON DELETE CASCADE,
    cve         text    NOT NULL COLLATE "C",
    title       text    NOT NULL,
    exploited   boolean NOT NULL,
    disclosed   boolean NOT NULL,
    exploit     text    NOT NULL, -- o texto original; vazio quando o MSRC não informa
    PRIMARY KEY (document_id, cve)
);
CREATE INDEX msrc_vulns_cve_idx ON msrc_vulns (cve);

CREATE TABLE msrc_affected (
    document_id text         NOT NULL,
    cve         text         NOT NULL COLLATE "C",
    product_id  text         NOT NULL COLLATE "C",
    severity    text         NOT NULL,
    impact      text         NOT NULL,
    base_score  numeric(3,1) NOT NULL,
    vector      text         NOT NULL,
    PRIMARY KEY (document_id, cve, product_id),
    FOREIGN KEY (document_id, cve) REFERENCES msrc_vulns ON DELETE CASCADE
);
CREATE INDEX msrc_affected_product_idx ON msrc_affected (product_id);

CREATE TABLE msrc_fixes (
    document_id      text NOT NULL,
    cve              text NOT NULL COLLATE "C",
    product_id       text NOT NULL COLLATE "C",
    kb               text NOT NULL,
    subtype          text NOT NULL,
    fixed_build      text NOT NULL, -- vazio quando o produto não tem build (Office, por exemplo)
    supersedes       text NOT NULL,
    restart_required text NOT NULL,
    PRIMARY KEY (document_id, cve, product_id, kb, fixed_build),
    FOREIGN KEY (document_id, cve) REFERENCES msrc_vulns ON DELETE CASCADE
);
CREATE INDEX msrc_fixes_product_idx ON msrc_fixes (product_id);
CREATE INDEX msrc_fixes_kb_idx      ON msrc_fixes (kb);
