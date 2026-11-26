-- 0085: implantação por prédio e andar (docs/implantacao-por-predio.md).
--
-- buildings dá a cada prédio do patrimônio (o DEPREDIO exato, sem normalizar: dois textos
-- diferentes são dois prédios) um ID estável. A importação do patrimônio acrescenta os novos,
-- marca os ausentes (present = false, sem apagar) e, quando um prédio some e outro aparece com
-- os mesmos bens, troca o nome mantendo o ID (renamed_from guarda o anterior). Janelas e
-- campanhas ficam ligadas ao ID, não ao nome.
CREATE TABLE buildings (
    id            bigserial   PRIMARY KEY,
    name          text        NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 200),
    present       boolean     NOT NULL DEFAULT true,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    renamed_from  text        NOT NULL DEFAULT '' CHECK (length(renamed_from) <= 200)
);

-- Os prédios do patrimônio atual; os que só aparecem em vínculos antigos (grupos, andares)
-- entram como ausentes, para o ID existir se o patrimônio voltar a tê-los.
INSERT INTO buildings (name)
    SELECT DISTINCT building FROM asset_locations WHERE building <> '' ON CONFLICT (name) DO NOTHING;
INSERT INTO buildings (name, present)
    SELECT DISTINCT building, false FROM building_rings WHERE building <> '' ON CONFLICT (name) DO NOTHING;
INSERT INTO buildings (name, present)
    SELECT DISTINCT building, false FROM floor_locations WHERE length(building) BETWEEN 1 AND 200 ON CONFLICT (name) DO NOTHING;

-- A janela de manutenção de cada prédio (vale para todos os andares dele), no horário local de
-- um fuso, como a dos grupos (0019). enabled = false guarda a configuração sem valer.
CREATE TABLE building_windows (
    building_id  bigint      PRIMARY KEY REFERENCES buildings (id) ON DELETE CASCADE,
    days         smallint    NOT NULL CHECK (days BETWEEN 1 AND 127),
    start_min    smallint    NOT NULL CHECK (start_min BETWEEN 0 AND 1439),
    duration_min smallint    NOT NULL CHECK (duration_min BETWEEN 10 AND 1440),
    timezone     text        NOT NULL CHECK (timezone <> ''),
    enabled      boolean     NOT NULL DEFAULT true,
    updated_by   text        NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- A campanha por localização: o escopo escolhido e as etapas (NULL: campanha legada, pelos
-- grupos de implantação de rings). As legadas não mudam.
ALTER TABLE campaigns ADD COLUMN scope jsonb;
ALTER TABLE campaigns ADD CONSTRAINT campaigns_scope_or_rings
    CHECK (scope IS NOT NULL OR cardinality(rings) > 0) NOT VALID;

-- Os alvos resolvidos na confirmação: uma linha por máquina (a chave impede a mesma máquina em
-- duas etapas), com o prédio e o andar daquela hora para o histórico. machine_id sem chave
-- estrangeira: a máquina excluída depois continua no histórico da campanha.
CREATE TABLE campaign_targets (
    campaign_id   bigint   NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    machine_id    text     NOT NULL CHECK (machine_id <> ''),
    stage_idx     smallint NOT NULL CHECK (stage_idx BETWEEN 0 AND 19),
    building_id   bigint   REFERENCES buildings (id) ON DELETE SET NULL,
    building_name text     NOT NULL DEFAULT '' CHECK (length(building_name) <= 200),
    floor_key     text     NOT NULL DEFAULT '' CHECK (length(floor_key) <= 300),
    floor_name    text     NOT NULL DEFAULT '' CHECK (length(floor_name) <= 300),
    via           text     NOT NULL CHECK (via IN ('predio', 'andar', 'sem_local')),
    PRIMARY KEY (campaign_id, machine_id)
);
CREATE INDEX campaign_targets_stage ON campaign_targets (campaign_id, stage_idx);
CREATE INDEX campaign_targets_building ON campaign_targets (building_id) WHERE building_id IS NOT NULL;
