-- 0026: aprovação de patches (Aula 13.1). Um item pendente da frota (pacote fonte do
-- Linux, ou CVE do Windows e do macOS) só entra em atualização e em campanha depois de
-- aprovado (Aula 13.2), e a aprovação guarda quem aprovou e quando.
--
-- A chave é o item MAIS a correção (fixed_in): aprovar o curl corrigido na
-- 8.18.0-1ubuntu2.10 não aprova a próxima correção do curl, que é outra mudança, a ser
-- olhada de novo. Revogar apaga a linha; o log do servidor guarda quem revogou.

CREATE TABLE patch_approvals (
    item        text        NOT NULL CHECK (length(item) BETWEEN 1 AND 200),
    fixed_in    text        NOT NULL CHECK (length(fixed_in) <= 200),
    approved_by text        NOT NULL CHECK (approved_by <> ''),
    approved_at timestamptz NOT NULL DEFAULT now(),
    note        text        NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    PRIMARY KEY (item, fixed_in)
);
