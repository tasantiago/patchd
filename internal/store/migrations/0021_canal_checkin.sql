-- 0021: por qual canal chegou o último check-in de cada máquina (Aula 9.2).
--
-- true = HTTPS (porta principal com TLS); false = HTTP (porta de transição, ou servidor
-- sem TLS); NULL = nenhum check-in desde esta versão. É o que diz quem ainda precisa
-- passar para HTTPS antes de fechar a porta de transição.

ALTER TABLE machines
    ADD COLUMN last_checkin_tls boolean;
