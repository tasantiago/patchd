-- 0051: desempenho e alertas (docs/monitoramento.md). As amostras de minuto ficam 7 dias; as
-- médias e picos por hora, 90; os alertas fechados, 90. A retenção monta as horas e apaga o
-- resto. perf_eval guarda a contagem da regra de cada máquina entre um check-in e outro.

CREATE TABLE perf_samples (
    machine_id text        NOT NULL REFERENCES machines (id),
    at         timestamptz NOT NULL,
    cpu        real        NOT NULL CHECK (cpu BETWEEN 0 AND 100),
    mem        real        NOT NULL CHECK (mem BETWEEN 0 AND 100),
    disks      jsonb       NOT NULL DEFAULT '{}',
    PRIMARY KEY (machine_id, at)
);
CREATE INDEX perf_samples_at ON perf_samples (at);

CREATE TABLE perf_hourly (
    machine_id text        NOT NULL REFERENCES machines (id),
    hour       timestamptz NOT NULL,
    cpu_avg    real        NOT NULL,
    cpu_max    real        NOT NULL,
    mem_avg    real        NOT NULL,
    mem_max    real        NOT NULL,
    disk_min   real,       -- o menor % livre entre os volumes na hora (nulo: sem volume)
    samples    integer     NOT NULL,
    PRIMARY KEY (machine_id, hour)
);

CREATE TABLE perf_eval (
    machine_id text        PRIMARY KEY REFERENCES machines (id),
    state      jsonb       NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE perf_alerts (
    id         bigserial   PRIMARY KEY,
    machine_id text        NOT NULL REFERENCES machines (id),
    metric     text        NOT NULL CHECK (metric IN ('cpu', 'mem', 'disk')),
    target     text        NOT NULL DEFAULT '',
    opened_at  timestamptz NOT NULL,
    closed_at  timestamptz,
    open_value real        NOT NULL,
    peak       real        NOT NULL,
    last_value real        NOT NULL,
    ack_by     text        NOT NULL DEFAULT '',
    ack_at     timestamptz,
    ack_note   text        NOT NULL DEFAULT ''
);
-- No máximo um alerta aberto por medida da máquina.
CREATE UNIQUE INDEX perf_alerts_um_aberto ON perf_alerts (machine_id, metric, target) WHERE closed_at IS NULL;
CREATE INDEX perf_alerts_maquina ON perf_alerts (machine_id, opened_at DESC);
CREATE INDEX perf_alerts_fechados ON perf_alerts (closed_at) WHERE closed_at IS NOT NULL;
