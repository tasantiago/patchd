CREATE TABLE discovery_credential (
 id integer PRIMARY KEY CHECK (id = 1),
 username text NOT NULL,
 password bytea NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 updated_by text NOT NULL
);
CREATE TABLE discovery_installs (
 ip inet PRIMARY KEY,
 state text NOT NULL,
 at timestamptz NOT NULL,
 started_by text NOT NULL
);
