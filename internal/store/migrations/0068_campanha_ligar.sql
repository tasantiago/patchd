-- 0068: ligar pela rede na janela da campanha e desligar depois (docs/wake-on-lan.md, Aula
-- 22.3). Com a opção, no início da janela (ou do horário fixo) o servidor pede para ligar as
-- máquinas do grupo que estão desligadas e têm job da campanha; quando os jobs dela terminam,
-- manda desligar só as que o patchd ligou e sem ninguém conectado. Uma linha por máquina e
-- grupo: o pedido (ou por que não saiu) e o desligar (o job, ou por que não).

ALTER TABLE campaigns ADD COLUMN wake boolean NOT NULL DEFAULT false;

CREATE TABLE campaign_wakes (
    campaign_id     bigint      NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    campaign_idx    smallint    NOT NULL,
    machine_id      text        NOT NULL REFERENCES machines (id),
    requested_at    timestamptz NOT NULL,
    wake_request_id bigint      REFERENCES wake_requests (id) ON DELETE SET NULL,
    problem         text        NOT NULL DEFAULT '',
    shutdown_job_id bigint      REFERENCES jobs (id) ON DELETE SET NULL,
    shutdown_note   text        NOT NULL DEFAULT '',
    PRIMARY KEY (campaign_id, campaign_idx, machine_id)
);
