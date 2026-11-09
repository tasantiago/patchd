-- 0066: token de registro pelo relé (docs/rele.md). O notebook que nunca entra na rede se
-- registra pela porta só de agentes; o token dele é de uso único e só vale por ela, e o token
-- comum continua só na rede interna.

ALTER TABLE enrollment_tokens
    ADD COLUMN relay boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT enrollment_tokens_relay_uso_unico CHECK (NOT relay OR max_uses = 1);
