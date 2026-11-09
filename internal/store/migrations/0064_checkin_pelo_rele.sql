-- 0064: por onde chegou o último check-in da máquina: pela porta só de agentes (o relé na
-- nuvem, notebooks fora da rede, docs/rele.md) ou pela porta de dentro. Nulo: nenhum check-in
-- desde esta versão.

ALTER TABLE machines ADD COLUMN last_checkin_relay boolean;
