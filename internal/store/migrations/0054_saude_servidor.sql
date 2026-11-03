-- 0054: a saúde do servidor (docs/saude-servidor.md). Uma linha por hora com o resumo dos
-- minutos medidos pelo próprio servidor (CPU, memória, requisições, pool do banco, disco e o
-- tamanho do banco no fim da hora), por 90 dias. Os minutos ficam só na memória do processo.

CREATE TABLE server_health_hourly (
    hour           timestamptz PRIMARY KEY,
    samples        integer     NOT NULL CHECK (samples > 0),
    cpu_avg        real        NOT NULL,
    cpu_max        real        NOT NULL,
    host_cpu_avg   real,       -- nulo: o sistema não mede a CPU da máquina
    host_cpu_max   real,
    mem_avg        real        NOT NULL,
    mem_max        real        NOT NULL,
    mem_bytes_max  bigint      NOT NULL,
    host_mem_max   real,
    requests       bigint      NOT NULL,
    slow           bigint      NOT NULL,
    errors         bigint      NOT NULL,
    lat_avg_ms     real        NOT NULL,
    lat_p95_max_ms real        NOT NULL,
    pool_in_use    integer     NOT NULL,
    pool_max       integer     NOT NULL,
    pool_waits     bigint      NOT NULL,
    disk_free_min  real,       -- nulo: nenhuma pasta de dados medida
    db_bytes       bigint      NOT NULL,
    goroutines     integer     NOT NULL
);
