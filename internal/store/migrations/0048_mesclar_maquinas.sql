-- Mesclagem lógica: relatórios, jobs, auditoria e envelopes cifrados mantêm os IDs
-- de origem. O registro incorporado é aposentado e sua credencial é revogada.
CREATE TABLE machine_merges (
 source_id text PRIMARY KEY REFERENCES machines(id),
 target_id text NOT NULL REFERENCES machines(id),
 merged_at timestamptz NOT NULL,
 merged_by text NOT NULL,
 reason text NOT NULL,
 CHECK (source_id <> target_id)
);
CREATE INDEX machine_merges_target ON machine_merges(target_id);
