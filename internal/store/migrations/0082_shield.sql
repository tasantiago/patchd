-- 0082: PatchD Shield — Missão 100% (docs/shield.md). A pontuação de proteção de cada máquina,
-- a sequência, o XP, as conquistas, as missões e as exceções. Independente do compliance: nada
-- aqui muda o estado de compliance das máquinas.

-- A política, versionada: cada gravação é uma versão nova (a anterior fica para a auditoria e
-- para saber com qual versão cada pontuação foi calculada). Sem linha: a política padrão,
-- desligada.
CREATE TABLE shield_policies (
    version    integer PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    body       jsonb NOT NULL,
    note       text NOT NULL DEFAULT '',
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A pontuação atual de cada máquina. details: os critérios (situação, responsabilidade, desde
-- quando, prazo). score nulo: nenhum critério conta (sem dado).
CREATE TABLE shield_scores (
    machine_id     text PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
    policy_version integer NOT NULL DEFAULT 0,
    score          numeric(5,2),
    sealed         boolean NOT NULL,
    overall        text NOT NULL,
    user_fault     boolean NOT NULL DEFAULT false,
    blame          text NOT NULL DEFAULT '',
    details        jsonb NOT NULL,
    evaluated_at   timestamptz NOT NULL,
    changed_at     timestamptz NOT NULL
);
CREATE INDEX shield_scores_overall ON shield_scores(overall);

-- O retrato do fim do dia de cada máquina (o último do dia, no fuso da política): a sequência
-- e os relatórios de evolução. Guardado por 400 dias.
CREATE TABLE shield_daily (
    machine_id     text NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    day            date NOT NULL,
    score          numeric(5,2),
    sealed         boolean NOT NULL,
    user_fault     boolean NOT NULL,
    no_data        boolean NOT NULL,
    ring           integer NOT NULL DEFAULT 1,
    policy_version integer NOT NULL DEFAULT 0,
    PRIMARY KEY (machine_id, day)
);
CREATE INDEX shield_daily_day ON shield_daily(day);

-- As pessoas. account: DOMINIO\usuario em minúsculas (a chave); sid: o do token do Windows,
-- quando a Central já falou com o agente (a conta renomeada continua a mesma pessoa).
CREATE TABLE shield_players (
    id           bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    account      text NOT NULL UNIQUE,
    sid          text UNIQUE,
    display_name text NOT NULL DEFAULT '',
    xp           bigint NOT NULL DEFAULT 0,
    streak       jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    seen_at      timestamptz NOT NULL DEFAULT now()
);

-- Quem usou qual máquina em qual dia (a sessão no check-in, ou a Central aberta): o dia de uma
-- máquina compartilhada conta para todos os que a usaram.
CREATE TABLE shield_presence (
    player_id  bigint NOT NULL REFERENCES shield_players(id) ON DELETE CASCADE,
    machine_id text NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    day        date NOT NULL,
    PRIMARY KEY (player_id, day, machine_id)
);
CREATE INDEX shield_presence_machine ON shield_presence(machine_id, day);

-- O XP: só eventos decididos pelo servidor. (player_id, kind, ref) é único: o mesmo evento
-- gravado duas vezes (dois servidores, reprocessamento) conta uma vez. Ajuste do administrador:
-- kind 'ajuste', com actor e reason obrigatórios (e a auditoria).
CREATE TABLE shield_xp (
    id         bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    player_id  bigint NOT NULL REFERENCES shield_players(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    ref        text NOT NULL,
    points     integer NOT NULL,
    day        date NOT NULL,
    reason     text NOT NULL DEFAULT '',
    actor      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (player_id, kind, ref),
    CHECK (kind <> 'ajuste' OR (actor <> '' AND reason <> ''))
);
CREATE INDEX shield_xp_day ON shield_xp(player_id, day);

-- As conquistas de cada pessoa (uma vez cada).
CREATE TABLE shield_badges (
    player_id bigint NOT NULL REFERENCES shield_players(id) ON DELETE CASCADE,
    code      text NOT NULL,
    earned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, code)
);

-- As missões. state: rascunho, publicada ou cancelada (agendada, ativa, concluída e encerrada
-- saem das datas e do progresso). scope: individual ou coletiva. goal: a meta (o critério e o
-- alvo). ring: o grupo de implantação (nulo: o parque do Shield inteiro).
CREATE TABLE shield_missions (
    id          bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    scope       text NOT NULL CHECK (scope IN ('individual', 'coletiva')),
    goal        jsonb NOT NULL,
    ring        integer,
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL CHECK (ends_at > starts_at),
    state       text NOT NULL CHECK (state IN ('rascunho', 'publicada', 'cancelada')),
    xp          integer NOT NULL DEFAULT 0 CHECK (xp BETWEEN 0 AND 10000),
    completed_at timestamptz,
    created_by  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- As exceções: um critério de uma máquina fora da conta até expires_at (com motivo e quem
-- aprovou). Revogada: revoked_at.
CREATE TABLE shield_exceptions (
    id         bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    machine_id text NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    criterion  text NOT NULL,
    reason     text NOT NULL CHECK (reason <> ''),
    expires_at timestamptz NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    revoked_by text NOT NULL DEFAULT ''
);
CREATE INDEX shield_exceptions_active ON shield_exceptions(machine_id) WHERE revoked_at IS NULL;
