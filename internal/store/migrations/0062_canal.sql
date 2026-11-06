-- 0062: canal em tempo real. Cada job criado avisa, pelo NOTIFY do PostgreSQL, os servidores
-- que escutam (LISTEN patchd_canal); quem tem o agente da máquina esperando o acorda. O aviso
-- só sai no COMMIT: quando o agente chega, o job já está lá. O conteúdo é só o ID da máquina.

CREATE FUNCTION patchd_canal_job() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('patchd_canal', NEW.machine_id);
    RETURN NULL;
END
$$;

CREATE TRIGGER jobs_canal AFTER INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION patchd_canal_job();

-- Os jobs agendados (janela, horário fixo) que acabaram de vencer o início: o servidor os
-- procura a cada 30 s para acordar as máquinas.
CREATE INDEX jobs_due_idx ON jobs (not_before) WHERE state = 'pendente' AND not_before IS NOT NULL;
