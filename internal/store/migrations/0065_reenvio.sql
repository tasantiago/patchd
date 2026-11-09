-- 0065: reenvio das falhas da campanha. auto_resend: quantas vezes o motor reenvia sozinho, a
-- cada máquina, o job que falhou ou expirou (0: nenhuma, como antes; o recusado não volta
-- sozinho). resend: o pedido do admin ("Reenviar" na página da campanha), os motivos a
-- reenviar no grupo de implantação atual; o motor pega e esvazia no próximo minuto.

ALTER TABLE campaigns
    ADD COLUMN auto_resend smallint NOT NULL DEFAULT 0 CHECK (auto_resend BETWEEN 0 AND 5),
    ADD COLUMN resend      text[]   NOT NULL DEFAULT '{}';
