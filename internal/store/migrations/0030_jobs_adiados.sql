-- 0030: jobs adiados (Aula 14.9). "adiado": o agente não executou agora e pede para tentar
-- depois (programa aberto, outro instalador rodando); a campanha pede de novo depois de
-- um intervalo. "dispensado": um job adiado cuja nova tentativa não fez mais sentido (o
-- plano da máquina mudou: em dia, sem o programa, sem pendência).
-- O nome da restrição é o que o PostgreSQL deu à CHECK da 0018 (jobs_state_check).

ALTER TABLE jobs DROP CONSTRAINT jobs_state_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_state_check
    CHECK (state IN ('pendente', 'entregue', 'concluido', 'falhou', 'rejeitado', 'cancelado', 'expirado', 'adiado', 'dispensado'));
