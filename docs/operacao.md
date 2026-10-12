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
| Token das métricas | `deploy/secrets/metrics_token` | gerar outro (apague o arquivo e rode `gerar-segredos.sh`) e trocar no Prometheus |
| Certificado TLS | `deploy/tls` | pedir outro à CA interna |
| Configuração | `deploy/.env` | refazer a partir de `deploy/.env.exemplo` |
| Chave privada de release | offline, fora do servidor | reinstalar a frota com agentes que confiem numa chave nova |
| Versões publicadas do agente | `releases/` | assinar e publicar de novo a partir do build |

## Métricas e alertas (Aula 10.2)

O servidor publica `GET /metrics` (formato de texto do Prometheus) numa porta só dele:
`PATCHD_METRICS_LISTEN_ADDR` (padrão do compose `:9090`; vazio desliga). Com TLS no
servidor, a porta é HTTPS com o mesmo certificado. Sem o token (`Authorization: Bearer`,
o conteúdo de `deploy/secrets/metrics_token`), a resposta é 401. O compose publica a porta
só em 127.0.0.1; para um Prometheus em outra máquina, `PATCHD_METRICS_BIND` com o IP do
host e o firewall liberando só o IP do Prometheus.

Conferir no próprio host:

```bash
# O cabeçalho vem de um arquivo (printf é do shell): o token não aparece no ps.
curl -s --cacert CA.crt -H @<(printf 'Authorization: Bearer %s\n' "$(cat deploy/secrets/metrics_token)") \
  https://127.0.0.1:9090/metrics | grep -E '^patchd_(machines|coleta_banco_ok|tls_cert)'
```

| Métrica | Tipo | O que diz |
|---|---|---|
| `patchd_http_requests_total{rota,codigo}` | contador | requisições por rota (o padrão, nunca o caminho com IDs) e código |
| `patchd_http_request_seconds_total{rota}` | contador | tempo gasto por rota (dividido pelas requisições: a média) |
| `patchd_checkins_total{canal}` | contador | check-ins por canal (https, http) |
| `patchd_jobs_encerrados_total{tipo,estado}` | contador | resultados de jobs vindos dos agentes |
| `patchd_logins_total{metodo,resultado}` | contador | logins no painel (senha ou rhbk; ok, recusado, bloqueado, sem_grupo) |
| `patchd_machines{canal}` | medidor | máquinas ativas pelo canal do último check-in (https, http, nenhum) |
| `patchd_compliance_machines{estado}` | medidor | máquinas por estado de compliance (RF-23) |
| `patchd_jobs{estado}` | medidor | jobs por estado |
| `patchd_catalog_last_success_timestamp_seconds{fonte}` | medidor | última sincronização boa de cada fonte |
| `patchd_catalog_last_run_ok{fonte}` | medidor | a execução mais recente deu certo (1) ou falhou (0) |
| `patchd_tls_cert_not_after_timestamp_seconds` | medidor | fim da validade do certificado em uso |
| `patchd_coleta_banco_ok`, `patchd_coleta_banco_segundos` | medidor | a leitura do banco para as métricas deu certo, e quanto levou |
| `patchd_build_info{version,commit}`, `patchd_start_time_seconds`, `go_goroutines` | medidor | versão, início do processo, goroutines |

Os medidores do banco são lidos no máximo uma vez por minuto (coletas mais frequentes
recebem os mesmos números). Banco fora: `patchd_coleta_banco_ok 0` e os medidores do banco
somem, em vez de mostrar números velhos. Os contadores recomeçam do zero quando o servidor
reinicia; `rate()` e `increase()` do Prometheus tratam isso.

Nenhuma métrica tem nome de máquina, ID, usuário ou IP como rótulo: o número de séries
fica fixo, e a saída pode ir para um chamado sem limpeza.

Prometheus: `deploy/prometheus/prometheus.exemplo.yml` (coleta com o token por arquivo) e
`deploy/prometheus/alertas.yml` (regras). Confira com
`promtool check config` e `promtool check rules` antes de recarregar.
