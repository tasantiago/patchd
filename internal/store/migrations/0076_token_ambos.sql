-- 0076: token de registro que vale na rede interna e pelo relé (docs/rele.md). Para instalar
-- de uma vez um lote de notebooks pela ferramenta de distribuição, sem saber quais estão
-- dentro da rede: o agente tenta o servidor direto e, sem resposta, o relé. Como o relé é a
-- porta que a internet alcança, o token tem usos contados (de 1 a 50) e validade curta (o
-- painel e a linha de comando limitam a 7 dias).
ALTER TABLE enrollment_tokens
    ADD COLUMN both_ports boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT enrollment_tokens_ambos CHECK (NOT both_ports OR (NOT relay AND max_uses BETWEEN 1 AND 50));
