-- 0016: quem aposentou a máquina (Aula 7.6): o usuário do painel, ou "linha de comando".

ALTER TABLE machines ADD COLUMN retired_by text;
