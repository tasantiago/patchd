-- 0078: a campanha de remoção (tipo desinstalar): o programa que ela tira do parque, como
-- campaign.RemoveTarget (o nome mostrado, o fabricante, os nomes exatos do inventário e as
-- versões; sem versões: todas). Nulo nas outras campanhas.

ALTER TABLE campaigns ADD COLUMN remove_target jsonb;
