-- 0036: notificações por e-mail (Aula 18.6). Cada aviso mandado deixa a chave aqui ("pedido:12",
-- "campanha:3:1760712345", "fonte:MAQUINA:wua-default:0"): o laço não manda o mesmo aviso
-- duas vezes, nem depois de reiniciar, nem com dois servidores no mesmo banco. As chaves
-- velhas saem com 90 dias.

CREATE TABLE notifications_sent (
    key     text        PRIMARY KEY,
    sent_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notifications_sent_at_idx ON notifications_sent (sent_at);
