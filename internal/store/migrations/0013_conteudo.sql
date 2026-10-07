-- 0013: arquivos que o servidor guarda para distribuir aos agentes (Aula 6.6, parte 3).
--
-- O conteúdo fica no disco (PATCHD_CONTENT_DIR), com o SHA-256 no nome; aqui fica o que
-- descreve a versão atual de cada arquivo. Por enquanto, só o wsusscn2.cab.

CREATE TABLE content_files (
    name          text        PRIMARY KEY COLLATE "C", -- wsusscn2
    file          text        NOT NULL,               -- nome no diretório: wsusscn2-<hash>.cab
    size          bigint      NOT NULL,
    sha256        text        NOT NULL,
    etag          text        NOT NULL,               -- da origem, com as aspas
    last_modified timestamptz,                        -- publicação na origem
    cabinet       bigint      NOT NULL,               -- tamanho do gabinete, sem a assinatura anexada
    fetched_at    timestamptz NOT NULL DEFAULT now()
);
