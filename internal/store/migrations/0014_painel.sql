-- 0014: usuários locais e sessões do painel (Aula 7.2).
--
-- Usuário local: a conta de quem administra o patchd sem depender do AD. Os usuários do
-- AD (Aula 7.3) não ficam aqui: o AD confere a senha e diz o perfil a cada login.
-- A senha nunca é guardada, só o hash PBKDF2 (internal/panel).

CREATE TABLE panel_users (
    username      text        PRIMARY KEY COLLATE "C",
    role          text        NOT NULL CHECK (role IN ('admin', 'leitura')),
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Sessão: o cookie leva o segredo; aqui fica só o SHA-256 dele, como nas credenciais das
-- máquinas. Sem chave estrangeira para panel_users: as sessões do AD não têm linha lá.
-- O perfil é o do momento do login: trocar a senha ou remover o usuário encerra as sessões.
CREATE TABLE panel_sessions (
    token_hash   bytea       PRIMARY KEY,
    username     text        NOT NULL COLLATE "C",
    role         text        NOT NULL CHECK (role IN ('admin', 'leitura')),
    source       text        NOT NULL CHECK (source IN ('local', 'ad')),
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL
);

CREATE INDEX panel_sessions_expires_idx ON panel_sessions (expires_at);
CREATE INDEX panel_sessions_user_idx ON panel_sessions (username);
