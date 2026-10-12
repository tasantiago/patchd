# Operação do patchd em produção

Guia de operação do compose de produção (`deploy/compose.yaml`). Nada aqui tem nome de
host, IP ou segredo do ambiente real: esses valores ficam em `deploy/.env` e
`deploy/secrets`, fora do git.

## Cópia do banco (Aula 10.1)

Tudo o que o patchd sabe sobre a frota está no PostgreSQL: máquinas, inventários, buscas,
jobs, campanhas, anéis e janelas, tokens de registro, usuários locais e sessões. O
catálogo (MSRC, USN, Fedora, Apple, KEV) também está, mas se refaz sozinho na próxima
sincronização.

- **Cópia:** `./deploy/backup.sh [PASTA]` (padrão `deploy/backups`, fora do git). O
  `pg_dump` roda dentro do container do Postgres, com o servidor no ar, e grava no formato
  custom. A cópia é conferida (`pg_restore --list`) antes de ganhar o nome final e recebe um
  `.sha256` e as contagens do momento ao lado. Cópias com mais de
  `PATCHD_BACKUP_KEEP_DAYS` dias (padrão 14) são apagadas.
- **Agenda:** `deploy/systemd/patchd-backup.{service,timer}` (todo dia às 02:30, com até
  15 minutos de sorteio; ajuste `WorkingDirectory`).
- **Fora do host:** uma cópia que só existe no mesmo disco do banco não protege contra a
  perda do host. Leve as cópias para outro lugar. A cópia tem hashes de senhas e de
  credenciais: pasta 700, arquivos 600 (cifrar as cópias: pendência).

## Ensaio de restauração (todo mês)

```bash
./deploy/restaurar.sh --testar deploy/backups/patchd-AAAAMMDDTHHMMSSZ.dump
```

Restaura num banco temporário, compara as contagens com as do momento da cópia e apaga o
banco temporário. O banco em uso não muda. Uma cópia que nunca foi restaurada não é uma
cópia.

## Restauração de verdade

```bash
./deploy/restaurar.sh --confirmo deploy/backups/patchd-AAAAMMDDTHHMMSSZ.dump
```

Confere o SHA-256, para o servidor, apaga e recria o banco `patchd`, restaura em uma
transação só e sobe o servidor, que aplica as migrations mais novas que a cópia. O que
aconteceu depois da cópia se perde: os agentes reenviam o inventário e a busca nos
próximos ciclos; jobs e campanhas criados depois da cópia precisam ser pedidos de novo.

## O que não está no banco

Guarde à parte, com o mesmo cuidado:

| Item | Onde | Se perder |
|---|---|---|
| Senha e URL do banco | `deploy/secrets/db_password`, `database_url` | gerar outra e trocar com `ALTER ROLE` |
| Chave de jobs | `deploy/secrets/job_signing_key` | os agentes recusam todo job até serem recompilados com a chave nova |
| Segredo do cliente do RHBK | `deploy/secrets/oidc_client_secret` | gerar outro no RHBK (aba Credentials) |
| Certificado TLS | `deploy/tls` | pedir outro à CA interna |
| Configuração | `deploy/.env` | refazer a partir de `deploy/.env.exemplo` |
| Chave privada de release | offline, fora do servidor | reinstalar a frota com agentes que confiem numa chave nova |
| Versões publicadas do agente | `releases/` | assinar e publicar de novo a partir do build |
