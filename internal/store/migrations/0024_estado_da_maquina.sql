-- 0024: estado volátil da máquina (Aula 12.1): rede e discos do último check-in, e o
-- endereço de onde ele veio.
--
-- Uma coluna, não uma tabela: o estado não tem histórico. Cada check-in sobrescreve o
-- anterior, e a tabela machines não cresce com o tempo (o inventário, que tem histórico,
-- continua em inventory_reports, versionado pelo hash).
--
-- last_checkin_addr é o endereço que o servidor viu na conexão (inet: dá para filtrar por
-- rede, ex.: last_checkin_addr << '10.0.0.0/8'). Atrás de um proxy reverso, é o do proxy.

ALTER TABLE machines
    ADD COLUMN state             jsonb,
    ADD COLUMN state_at          timestamptz,
    ADD COLUMN last_checkin_addr inet;
