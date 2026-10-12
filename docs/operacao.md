# Operação do patchd em produção

Guia de operação do compose de produção (`deploy/compose.yaml`). Nada aqui tem nome de
host, IP ou segredo do ambiente real: esses valores ficam em `deploy/.env` e
`deploy/secrets`, fora do git.

## Comandos do compose e atualização (Aula 10.4)

Variáveis `PATCHD_*` exportadas no shell valem mais que o `deploy/.env` na interpolação do
compose: um `docker compose up` digitado num terminal com o `.env` do desenvolvimento
carregado sobe a produção com a configuração errada. Por isso, em produção:

- `./deploy/compose.sh ARGS` no lugar de `docker compose -f deploy/compose.yaml ARGS`
  (`ps`, `logs --tail 50 server`, `restart server`…): o mesmo comando, sem nenhuma
  `PATCHD_*` do shell. Os outros scripts do `deploy/` fazem o mesmo.
- `./deploy/atualizar.sh vX.Y.Z` para trocar de versão (e na primeira subida):
  1. confere a árvore limpa e constrói `patchd-server:vX.Y.Z` com o commit atual, com o
     servidor antigo ainda no ar;
  2. faz uma cópia do banco (cifrada, se houver `backup_recipients`);
  3. anota versão e commit em `deploy/.env` e troca o servidor, esperando o healthcheck;
  4. diz se o banco mudou (migration antes e depois).

  A descrição da troca fica em `deploy/.atualizacao` (fora do git). Se o servidor novo não
  ficar saudável, o script para, mostra o fim do log e não desfaz nada sozinho.
- `./deploy/atualizar.sh --voltar`: se o banco não mudou, só troca a imagem de volta. Se
  mudou (migration nova), o servidor antigo não sobe nele (ele recusa um banco com
  migration que não conhece), e a volta exige restaurar a cópia de antes:
  `--voltar --confirmo`, com `PATCHD_BACKUP_IDENTITY_FILE` se a cópia for cifrada. O que
  aconteceu depois da cópia se perde; os agentes reenviam inventário e busca nos
  próximos ciclos.

A imagem leva a versão e o commit nos rótulos (`org.opencontainers.image.version` e
`.revision`). Imagens antigas ficam no host até alguém apagar: sem a imagem anterior, não
há volta rápida (`docker image ls patchd-server`; apague só as que não serão mais
voltadas).

## Cópia do banco (Aula 10.1)

Tudo o que o patchd sabe sobre a frota está no PostgreSQL: máquinas, inventários, buscas,
jobs, campanhas, anéis e janelas, tokens de registro, usuários locais e sessões. O
catálogo (MSRC, USN, Fedora, Apple, KEV) também está, mas se refaz sozinho na próxima
sincronização.

- **Cópia:** `./deploy/backup.sh [PASTA]` (padrão `deploy/backups`, fora do git). O
  `pg_dump` roda dentro do container do Postgres, com o servidor no ar, e grava no formato
  custom. A cópia é conferida (`pg_restore --list`) antes de ganhar o nome final e recebe um
  `.sha256` e as contagens do momento ao lado. Cópias com mais de
  `PATCHD_BACKUP_KEEP_DAYS` dias (padrão 14) são apagadas. Cada cópia é registrada no banco
  (tabela `backup_runs`) e aparece nas métricas (`patchd_backup_last_*`).
- **Agenda:** `deploy/systemd/patchd-backup.{service,timer}` (todo dia às 02:30, com até
  15 minutos de sorteio; ajuste `WorkingDirectory`).
- **Fora do host:** uma cópia que só existe no mesmo disco do banco não protege contra a
  perda do host. Leve as cópias para outro lugar. A cópia tem hashes de senhas e de
  credenciais: pasta 700, arquivos 600, e cifrada (próxima seção) antes de sair do host.

## Cópias cifradas (Aula 10.3)

Com `deploy/secrets/backup_recipients`, o `backup.sh` cifra a cópia com o
[age](https://age-encryption.org) para as chaves **públicas** listadas ali e grava
`patchd-AAAAMMDDTHHMMSSZ.dump.age`. O servidor só cifra; decifrar exige a chave
**privada**, que nunca fica no servidor. Quem rouba o disco, a pasta de cópias ou o
destino fora do host leva arquivos que não abre.

1. Numa máquina de administração, fora do servidor, gere dois pares (dois, porque perder
   a única chave privada torna todas as cópias inúteis):
   ```bash
   age-keygen -o patchd-copias-A.txt      # imprime "Public key: age1…"
   age-keygen -o patchd-copias-B.txt
   ```
   Guarde as privadas em lugares diferentes: por exemplo, uma no cofre de senhas da equipe
   e outra em mídia offline. Para guardar a privada com senha (age 1.1 ou mais novo):
   `age -p -o patchd-copias-A.age patchd-copias-A.txt` e apague o `.txt`; o
   `age -d -i patchd-copias-A.age` pede a senha.
2. No servidor, só as públicas, uma por linha (`#` comenta):
   ```bash
   sudo apt install age
   printf '# operador\nage1…\n# cofre\nage1…\n' > deploy/secrets/backup_recipients
   ./deploy/gerar-segredos.sh            # "cópias: cifradas com age para 2 destinatário(s)."
   ```
   O `backup.sh` e o `gerar-segredos.sh` recusam o arquivo se ele tiver uma chave privada
   (`AGE-SECRET-KEY-…`).
3. Ensaio e restauração de uma cópia cifrada: a chave privada entra só durante o ensaio.
   ```bash
   PATCHD_BACKUP_IDENTITY_FILE=/media/chave/patchd-copias-A.txt \
     ./deploy/restaurar.sh --testar deploy/backups/patchd-AAAAMMDDTHHMMSSZ.dump.age
   ```
   O texto claro vai do `age` direto para dentro do container do Postgres, sem passar
   pelo disco do host. Chave de outro par ou arquivo adulterado: o `age` para com erro e
   nada é restaurado. Com `--confirmo`, a cópia é decifrada e lida (`pg_restore --list`)
   antes de o banco em uso ser apagado.

Trocar as chaves: gere o par novo, ponha a pública em `backup_recipients` (pode ficar ao
lado da antiga por um tempo) e guarde a privada antiga enquanto houver cópias feitas
com ela.

Depois de uma restauração, a tabela `backup_runs` volta ao estado da cópia: a métrica
mostra a cópia anterior até a próxima execução do `backup.sh`.

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
| Chaves privadas das cópias (age) | fora do servidor (cofre e mídia offline) | sem nenhuma das duas, as cópias cifradas não abrem: gere um par novo e faça uma cópia já |
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

Cópias do banco (Aula 10.3): `patchd_backup_last_success_timestamp_seconds`,
`patchd_backup_last_size_bytes` e `patchd_backup_last_encrypted`, da última linha de
`backup_runs`. Sem nenhuma cópia registrada, as três não existem (alerta
`PatchdNenhumaCopia`).

Prometheus: `deploy/prometheus/prometheus.exemplo.yml` (coleta com o token por arquivo) e
`deploy/prometheus/alertas.yml` (regras). Confira com
`promtool check config` e `promtool check rules` antes de recarregar.
