# Operação do patchd em produção

Guia de operação do compose de produção (`deploy/compose.yaml`). Nada aqui tem nome de
host, IP ou segredo do ambiente real: esses valores ficam em `deploy/.env` e
`deploy/secrets`, fora do git.

No RHEL, com Podman sem root e Quadlet (Aula 23.1): `docs/podman.md`. Lá, o agente também
sai do próprio servidor, sem o ambiente de desenvolvimento: `./deploy/agente.sh` (Aula 23.2). O `atualizar.sh`, o
`backup.sh`, o `restaurar.sh` e o `gerar-segredos.sh` são os mesmos (o `PATCHD_MOTOR=podman`
do `.env` decide); no lugar do `compose.sh`, os comandos da tabela de lá.

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
há volta rápida (`docker image ls patchd-server`, ou `podman image ls patchd-server`;
apague só as que não serão mais voltadas). Na primeira subida, com o banco vazio, não há
cópia antes da troca, nem volta (Aula 23.1).

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

## Retenção do banco (Aula 10.5)

O banco guarda um inventário novo a cada mudança de software e uma busca por dia de cada
máquina; sem limpeza, essas duas tabelas crescem para sempre. O servidor apaga, de hora em
hora:

| O quê | Depois de | Fica sempre |
|---|---|---|
| inventários e buscas | `PATCHD_RETENTION_HISTORY_DAYS` (padrão 90; 30 a 3650) | o atual de cada máquina, por mais velho que seja |
| jobs encerrados | `PATCHD_RETENTION_JOBS_DAYS` (padrão 400; 0 = sempre) | pendentes, entregues e os de campanha que ainda não acabou |
| campanhas encerradas | o mesmo prazo dos jobs, e só quando não sobrou job delas | em andamento e pausadas |

Ficam de fora: máquinas (inclusive aposentadas), tokens de registro, usuários e o
catálogo (este se refaz sozinho). As sessões do painel e as execuções do catálogo já têm a
limpeza delas.

Antes de mudar os prazos, veja o efeito (nada é apagado sem `-apply`):

```bash
./deploy/compose.sh exec server /patchd-server retention
```

Apagar não encolhe os arquivos do PostgreSQL: o espaço das linhas apagadas é reaproveitado
pelas linhas novas (autovacuum), e o banco **para de crescer**. Para devolver o espaço ao
disco é preciso `VACUUM FULL`, que trava a tabela enquanto roda: só numa janela, e só se
o disco precisar. Acompanhe por `patchd_db_bytes` e `patchd_db_table_bytes{tabela}`; uma
tabela que cresce sem parar depois da retenção é uma tabela sem retenção.

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
| `patchd_agente_limitado_total{classe}` | contador | requisições de máquinas recusadas pelo limite por máquina (Aula 20.5; relatorio ou geral). Subindo: uma máquina fora do normal (o log diz qual, uma vez a cada 10 min) |
| `patchd_machines{canal}` | medidor | máquinas ativas pelo canal do último check-in (https, http, nenhum) |
| `patchd_compliance_machines{estado}` | medidor | máquinas por estado de compliance (RF-23) |
| `patchd_jobs{estado}` | medidor | jobs por estado |
| `patchd_catalog_last_success_timestamp_seconds{fonte}` | medidor | última sincronização boa de cada fonte |
| `patchd_catalog_last_run_ok{fonte}` | medidor | a execução mais recente deu certo (1) ou falhou (0) |
| `patchd_tls_cert_not_after_timestamp_seconds` | medidor | fim da validade do certificado em uso |
| `patchd_db_bytes`, `patchd_db_table_bytes{tabela}` | medidor | tamanho do banco e de cada tabela, com índices (Aula 10.5) |
| `patchd_retencao_apagados_total{tabela}` | contador | linhas apagadas pela retenção (Aula 10.5) |
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

## Fonte de busca falhando (Aula 16.2)

O painel avisa, na máquina e na Visão geral, quando a busca atual falhou numa fonte, com a
última busca em que ela respondeu. A fonte da política (`wua-default`: WSUS ou Microsoft) é
erro: sem ela, a máquina não recebe nada pelo Windows Update, e o job `windows_update` nem é
criado (`job create` explica). O catálogo offline é atenção.

Primeiro passo, na máquina (PowerShell como administrador): a mesma busca, fora do patchd.

```powershell
$q = (New-Object -ComObject Microsoft.Update.Session).CreateUpdateSearcher()
$q.Search("IsInstalled=0 and IsHidden=0").Updates.Count
```

Se falhar, o motivo está no WindowsUpdate.log (`Get-WindowsUpdateLog`); procure `SOAP` e
`ErrorCode`. Caso do lab (outubro de 2026): `0x80244007` com
`InvalidParameters: parameters.OtherCachedUpdateIDs` era o WSUS recusando a sincronização
(o catálogo dele passou do limite que ele aceita do cliente); a correção foi no WSUS, não
na máquina. Limpar a `SoftwareDistribution` da máquina não resolve esse caso: na primeira
sincronização vem o `0x80244010` (idas e voltas demais) e, com o cache cheio de novo, o
mesmo `0x80244007`.

## Formulário da campanha (Aula 16.6)

O formulário de Automação › Campanhas fica em coluna: o tipo em cartões com uma linha de
explicação, e só os campos do tipo escolhido à vista (Programa no de terceiros; Fontes ou KBs
no atualizar pacotes e no Windows; Aviso no reiniciar). Os anéis sem máquinas ficam em
"Anéis sem máquinas", recolhidos. Tudo por CSS (`:has()`), sem JavaScript, dentro da CSP do
painel; num navegador sem `:has()`, todos os campos aparecem, como antes. O servidor ignora
os campos que não valem para o tipo, então nada muda na conferência. `?tipo=reboot` abre
com o reinício escolhido.

## Autoatendimento: o canal local no Windows (Aula 17.3)

O agente do Windows atende o app do usuário por um named pipe, `\\.\pipe\patchd-autoatendimento`,
e fala com o servidor com a credencial da máquina. Quem pede não é algo que o app informa:
o agente lê a conta e os grupos do token do Windows de quem abriu o pipe.

- Quem pode abrir: usuários com sessão interativa (console ou área de trabalho remota). O
  pipe recusa conexões de outra máquina (`PIPE_REJECT_REMOTE_CLIENTS` e um ACE negando
  LOGON DE REDE). Um processo de serviço, ou uma tarefa agendada sem sessão, é recusado
  mesmo como administrador. O usuário pode abrir o canal, mas não pode criar uma instância
  dele: sem `FILE_CREATE_PIPE_INSTANCE`, ninguém se passa pelo agente.
- Se outro processo já tiver um pipe com esse nome quando o agente sobe, o agente não
  atende. O log mostra `autoatendimento: canal local fechado` com o motivo.
- O app só lê o token com nível de identificação: o agente sabe quem é o usuário, mas não
  age como ele. O app confere se o dono do pipe é o SYSTEM ou o grupo Administradores antes
  de mandar o pedido.
- Grupos que contam: os ligados no token, inclusive os aninhados, que o controlador de
  domínio resolveu no logon. Ficam de fora os "só para negar" (o Administradores de quem
  está com o UAC). Quem entrou num grupo novo precisa sair e entrar de novo na sessão. O
  nome do grupo é resolvido pelo controlador, ou pelo cache do Windows; sem resposta, o
  grupo fica de fora da loja daquela vez.
- Limites, por usuário: 5 pedidos (atualizar, instalar) e 60 consultas (loja) a cada 10
  minutos. A máquina inteira atende 4 pedidos ao mesmo tempo; além disso, a resposta é
  "ocupado".
- Pedido aceito (job criado): o agente faz o check-in na hora, sem esperar a próxima hora,
  com no mínimo 30 s entre um check-in e outro. O job chega e a instalação começa.

Na máquina, na sessão do usuário (sem elevar): `patchd-agent selfservice whoami`,
`selfservice catalog` e `selfservice request -app ID -action atualizar|instalar`. No log do
agente (`C:\ProgramData\patchd\logs\agent.log`), procure por `autoatendimento`.

## Central de software, o app do usuário (Aula 17.4)

No Windows, o serviço do agente põe a Central de software para abrir no logon de todos os
usuários: a chave `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, valor
`patchd-central-de-software`, aponta para `C:\Program Files\patchd\patchd-app.exe app`. O
`patchd-app.exe` é uma cópia do agente feita pelo serviço: com o app aberto nas sessões, o
`patchd-agent.exe` continua livre para a atualização automática. A cópia é marcada como
programa gráfico (um campo do cabeçalho do executável), para o logon não abrir uma janela de
console. Cópia em uso é renomeada (`.old-...`) e sai quando ninguém mais a usa.

- O app mostra o escudo perto do relógio. Clique abre a página no navegador padrão; o menu
  (botão direito) tem "Fechar este ícone". Quando aparece atualização nova para o usuário,
  vem um aviso do Windows (até 2 min depois do logon e a cada 4 h).
- A página é servida só em `127.0.0.1`, numa porta do sistema, sob um caminho com um segredo
  novo a cada sessão. Sem o segredo, nada responde; o Host e a origem dos formulários são
  conferidos. A página não tem JavaScript; durante uma instalação, ela se atualiza sozinha.
- Desligar (servidores, máquinas sem usuário): o serviço com `-user-app=false` (ou
  `PATCHD_USER_APP=0`). No próximo início, o serviço tira a chave e a cópia.
- `patchd-agent app -no-tray` serve a página sem ícone e mostra o endereço no terminal.

### Organizações aceitas nos instaladores (Aula 20.5b)

Com `-allowed-signers`, o agente do Windows só executa instalador de terceiros (job do
painel, campanha ou pedido da Central) com assinatura Authenticode válida de uma das
organizações da lista. É a defesa contra quem tem a chave de jobs (o servidor comprometido,
ou a chave roubada): o job pode dizer o que quiser, a lista é da máquina.

- Na instalação (ou numa reinstalação, que mantém a credencial), pela flag, que vai para a
  linha de comando do serviço:
  ```
  patchd-agent.exe install -allowed-signers "Google LLC;Mozilla Corporation;Microsoft Corporation;NOTEPAD++;The Document Foundation"
  ```
  A variável `PATCHD_ALLOWED_SIGNERS` vale para quem roda o agente à mão; o serviço guarda
  só a flag.
- O nome é o O= do certificado, exatamente como o `signer` do manifesto (Patch › Terceiros ›
  programa, "assinatura exigida: …").
- Programa sem assinatura (o 7-Zip) deixa de ser instalado pelo patchd nas máquinas com a
  lista: o job volta "rejeitado", com o motivo.
- O resumo da máquina no painel mostra a lista ("Instaladores aceitos"); sem ela, o aviso
  "qualquer assinatura". Agentes anteriores à v0.17.8 não informam (a linha não aparece).

### Pasta de dados do agente (Aula 20.6)

- O install e a partida do serviço conferem a pasta de dados: ela não pode ser link nem
  junção (o install recusa: confira quem a criou, apague e rode de novo); o dono passa a ser
  Administradores (root no Linux e no macOS), e tudo dentro volta a herdar só dela. O que foi
  corrigido sai no log do agente: "pasta de dados corrigida na partida", com o detalhe.
- Um "dono trocado" numa máquina recém-instalada por um administrador é normal (o Windows
  põe a conta dele como dona). Numa máquina em que ninguém mexeu, ou um "link ou junção
  apagado", investigue: alguém preparou a pasta antes do patchd.
- O TEMP dos instaladores é `pasta de dados\tmp\job-N`; as pastas com mais de 7 dias saem
  no job seguinte.

### Loja e Meus pedidos (Aula 17.5)

- **Loja**: o que a oferta deixa o usuário instalar do zero, com uma confirmação antes do
  pedido; o que já está instalado; e o que está oferecido, mas indisponível agora (o motivo é
  o mesmo da prévia em Configuração › Autoatendimento).
- **Meus pedidos**: os 10 pedidos mais recentes do usuário naquela máquina (o servidor filtra
  pelo autor `autoatendimento: DOMINIO\usuario`), com a situação do job. Outro usuário da
  mesma máquina não vê os pedidos de quem não é ele. "Adiado" é o programa aberto na hora da
  instalação: o pedido não volta sozinho para a fila; o usuário fecha o programa e pede de
  novo.
- Depois de um pedido aceito, o app confere a cada 30 s até o job terminar e avisa no ícone
  ("pronto", "adiado" ou "não deu certo").
- Agentes até a v0.17.1 não repassam os pedidos ao app (o campo é novo no protocolo).

### Reinício na hora do usuário (Aula 17.6)

- A aba **Reinício** aparece com o reinício pendente lido na hora pelo agente (a loja do
  servidor traz o da última busca, que pode ter um dia). O usuário escolhe: agora (2 min de
  aviso), daqui a 1, 2 ou 4 horas, ou 12:00 e 18:00 de hoje, até 12 h à frente.
- O pedido vira um job `reboot` com a hora (`at`), autor `autoatendimento: DOMINIO\usuario`,
  na auditoria como os outros pedidos. O agente agenda o `shutdown /r` para aquela hora; o
  Windows mostra o aviso. Sem reinício pendente na hora em que o job chega, ele volta
  dispensado, como na 16.5.
- Dois reinícios na mesma máquina (o do usuário e o de uma campanha, por exemplo): vale o
  mais cedo. O agente cancela o mais tarde, se for o caso, e o job dele volta dispensado
  ("já havia um reinício agendado para antes"), sem pausar o anel da campanha.
- Em servidor (Windows Server), o app mostra que o reinício é com a TI, e o servidor recusa
  o pedido.
- O ícone lembra do reinício pendente sem hora escolhida a cada 4 h; o clique abre a aba.
- O pedido com hora exige o agente v0.17.3: os anteriores recusam o campo `at`.

### O autoatendimento no painel (Aula 17.7)

Automação › Autoatendimento mostra, para os dois perfis, o que os usuários pediram nos
últimos 7, 30 ou 90 dias: os pedidos que viraram job (autor `autoatendimento: ...`), com o
programa e a versão dos parâmetros assinados, e como terminaram (em andamento, concluídos,
adiados, não deram certo), os programas mais pedidos e as recusas recentes (da auditoria).
Os indicadores filtram a tabela. Muitos "adiados" de um programa pedem uma campanha fora do
expediente; "não deram certo" repetidos num programa pedem olhar o instalador aprovado.

## Auditoria: retenção e linha de comando (Aula 18.1)

- **Retenção da trilha**: `PATCHD_RETENTION_AUDIT_DAYS` (padrão 1825 dias, 5 anos; `0` guarda
  sempre; de 365 a 3650). A limpeza é a mesma de hora em hora da Aula 10.5, e
  `patchd-server retention` mostra antes quantos eventos sairiam. Quando apaga, a própria
  limpeza entra na trilha (`auditoria.retencao`, usuário `patchd-server`, origem `sistema`),
  na mesma transação: não existe trilha apagada sem o registro de que foi apagada. O prazo é
  decisão da instituição: confira com a norma interna de guarda de registros antes de mudar.
- **Linha de comando**: os subcomandos que mudam alguma coisa (job create/cancel/keygen,
  campaign create/pause/resume/cancel, ring set/window, machine retire/restore, token
  create/revoke, user create/password/delete, package fetch/install/approve/revoke, release
  publish/withdraw, retention -apply, repo-cache prune) viram `cli.COMANDO.SUBCOMANDO`, com
  origem "linha de comando", as opções (valores de opções com senha, token ou chave no nome
  saem como `***`) e o resultado. Consultas e erros de uso não entram.
- **Quem rodou**: `PATCHD_OPERATOR`, se definido (no container todos são o mesmo usuário do
  sistema; ponha no `.env` o nome que você usa no painel); senão, o usuário do sistema.
- Painel: Configuração › Auditoria, grupos "Linha de comando" e "Retenção da auditoria";
  terminal: `patchd-server audit -action cli.`.

## Arremates da Aula 18.2

- **Usuários conectados no macOS**: o agente lê o `who` (o leitor oficial do utmpx do
  macOS), marca como ativo no console só o dono do `/dev/console` (com a troca rápida de
  usuários, os outros aparecem desconectados; na tela de login, que é do root, todos os
  consoles contam como ativos) e pega o nome completo pelo `id -F`. Exige o agente v0.17.4
  no Mac. Para conferir: `sudo patchd-agent -state` mostra a lista em `users`.
- **Firefox ESR**: o instalador tem de ser assinado pela Mozilla Corporation, como o do
  Firefox (medido no lab da 18.2 com `Get-AuthenticodeSignature`). Um instalador com outro
  assinante é recusado pelo agente.
- **`PATCHD_PANEL_TITLE`**: um par de aspas em volta do valor sai (o Compose passa o valor do
  `.env` como está escrito, aspas incluídas); de 1 a 40 letras (não bytes: acentos contam
  como uma). Fora disso, o log avisa e o painel fica com "patchd".

## Autoatendimento com aprovação da TI (Aula 18.3)

- Em Configuração › Autoatendimento, a caixa **Aprovação da TI** faz cada pedido daquele
  programa esperar alguém aprovar. O pedido guarda a conta e os grupos do token no momento
  em que foi feito, o programa, a ação e a versão que a loja oferecia.
- A fila fica em Automação › Autoatendimento, "Aguardando a TI" (os dois perfis veem; só o
  admin decide). **Aprovar** refaz a loja com a conta e os grupos guardados: se o programa
  saiu da loja desse usuário, ou mudou na máquina (já instalado, já em dia), a aprovação não
  passa e o pedido continua na fila para ser recusado com o motivo. Passando, o job sai como
  se o usuário tivesse pedido agora, com ele como autor (o usuário acompanha em Meus
  pedidos), e a decisão vai para a auditoria (`autoatendimento.aprovar`). **Recusar** leva
  um motivo opcional (até 200 letras), que o usuário vê em Meus pedidos
  (`autoatendimento.reprovar`).
- Sem decisão em 7 dias, o pedido expira. Um segundo pedido do mesmo programa, com o
  primeiro esperando, não cria outro.
- O job aprovado chega à máquina no próximo check-in (até 1 hora); o app da 18.4 antecipa.
- Na auditoria, o pedido guardado é `autoatendimento.solicitacao`.

### A aprovação da TI no app (Aula 18.4)

- Na Central de software, o programa com aprovação mostra "com aprovação da TI" e o botão
  **Pedir à TI**; com um pedido esperando, o selo "Com a TI (pedido nº N)" no lugar do botão.
  Em Meus pedidos, o pedido à TI aparece como "Com a TI", "Recusado pela TI" (com o motivo)
  ou "Expirou sem resposta da TI"; aprovado, ele vira o job e segue como os outros.
- Com pedido esperando, o app confere a cada 5 minutos e avisa no ícone quando a TI decide
  ("Resposta da TI").
- Quando a loja mostra um job do usuário ainda não entregue (a TI acabou de aprovar), o
  agente faz o check-in na hora: a instalação começa minutos depois da aprovação, sem
  esperar a próxima hora. Exige o agente v0.17.5.

## Notificações por e-mail (Aula 18.5)

- Configuração no `.env` (os valores do ambiente real ficam só lá):
  `PATCHD_SMTP_ADDR` (host:porta; vazio desliga), `PATCHD_SMTP_FROM`, `PATCHD_NOTIFY_TO`
  (vírgula entre os endereços), `PATCHD_SMTP_TLS` (`starttls`, o padrão; `tls` para a porta
  465; `none` só para um relay interno que não oferece TLS), `PATCHD_SMTP_USER` e a senha por
  `PATCHD_SMTP_PASSWORD_FILE` (no compose, `deploy/secrets/smtp_password`), e
  `PATCHD_SMTP_CA_FILE` quando o SMTP usa a CA interna.
- Com `starttls`, o servidor que não oferece STARTTLS é recusado: a mensagem não sai em
  texto claro por engano. Usuário e senha com `none` é erro de configuração.
- Configuração errada (endereço sem porta, destinatário inválido, usuário sem senha, CA
  ilegível) não deixa o servidor subir; o log da partida resume a configuração, sem a senha.
- Teste: Configuração › Sistema › **Mandar e-mail de teste** (só admin; um a cada 30 s;
  entra na auditoria como `notificacao.teste`, com o resultado) ou, no terminal,
  `patchd-server notify status` e `patchd-server notify test` (`cli.notify.test`).
- "Aceita pelo SMTP" quer dizer que o servidor de e-mail recebeu; se não chegar à caixa, o
  problema está depois dele (filtro, regra de encaminhamento, lista).
- As mensagens levam `Auto-Submitted: auto-generated`: respostas automáticas (férias) não
  voltam para o remetente.

### Avisos automáticos (Aula 18.6)

Com as notificações ligadas e o PostgreSQL, o servidor confere a cada 5 minutos e manda:

| Aviso (`PATCHD_NOTIFY_EVENTS`) | Quando | Um e-mail por |
|---|---|---|
| `pedidos` | pedido do autoatendimento novo esperando a TI | passada (lista os novos e o total da fila) |
| `campanhas` | o servidor pausou a campanha (job sem sucesso, sem janela, instalador fora) nos últimos 7 dias; a pausa feita pelo admin não avisa | campanha pausada |
| `fontes` | a máquina passou a falhar na busca da fonte principal (`wua-default`, apt, dnf, softwareupdate); o catálogo offline não avisa | passada (lista as novas) |

- Cada aviso tem uma chave guardada em `notifications_sent` (90 dias). A mesma situação não
  avisa de novo: a fonte que continua falhando fica quieta; se voltar a responder e falhar
  outra vez, é aviso novo. A campanha retomada e pausada de novo também.
- SMTP fora do ar: nada é marcado, o log avisa uma vez ("a passada falhou") e os avisos saem
  na primeira passada depois que o SMTP voltar.
- Dois servidores no mesmo banco: a trava consultiva deixa um só fazer a passada.
- `patchd-server notify pending` mostra os e-mails que a próxima passada mandaria, sem mandar
  nem marcar. Para parar um tipo: tire-o de `PATCHD_NOTIFY_EVENTS` e suba o servidor de novo.

## A fila da TI pelo terminal (Aula 18.7)

O mesmo que Automação › Autoatendimento faz, sem o painel (no compose:
`./deploy/compose.sh exec server /patchd-server selfservice ...`):

```
patchd-server selfservice requests            # os que aguardam a TI
patchd-server selfservice requests -all       # também os decididos (os 50 mais novos)
patchd-server selfservice approve -id 12
patchd-server selfservice refuse -id 13 -note "fora do escopo do setor"
```

- A aprovação refaz a loja com a conta e os grupos guardados, como no painel; não passando,
  o motivo sai no terminal e o pedido continua na fila.
- A decisão entra na trilha com a mesma ação do painel (`autoatendimento.aprovar` ou
  `.reprovar`), a origem "linha de comando" e quem rodou (`PATCHD_OPERATOR` ou o usuário do
  sistema). O e-mail da 18.6 lembra o comando.
- O `selfservice catalog` mostra o pedido que espera como "pedido à TI nº N" (antes saía
  "job 0").

## Ofertas em lote e a Central renovada (Aula 18.8)

- Configuração › Autoatendimento é um formulário só: mude as caixas e os grupos de quantas
  linhas quiser e clique em **Salvar alterações**. Só as linhas que mudaram são gravadas e
  vão para a auditoria (`autoatendimento.oferta` ou `.remover`, com "em lote"), mais um
  resumo `autoatendimento.oferta_lote`.
- **Em lote**: marque os programas na primeira coluna (ou escolha "todos os programas") e
  escolha o que aplicar: atualizar, instalar, aprovação da TI e grupos, cada um com "manter".
  O lote vale por cima do que está na tela.
- Tudo ou nada: um grupo inválido em qualquer linha e nada é gravado; a página volta com o
  que foi digitado e a linha marcada.
- A Central de software confere a cada minuto se a cópia `patchd-app.exe` foi trocada por
  uma atualização do agente e, se foi, reabre sozinha com a nova (o ícone some e volta; a
  página aberta no navegador para de responder: abra de novo pelo ícone). Se a cópia sumir
  (agente removido, `-user-app=false`), a Central fecha. Vale a partir da release do agente
  com a Aula 18.8; a Central aberta antes dela ainda precisa de um último logon.

## Layout do tribunal e logotipo (Aula 19.1)

- O painel segue o padrão das aplicações do tribunal: barra lateral azul-marinho com o
  logotipo, o usuário (nome, perfil e origem; sem foto) e os grupos NAVEGAÇÃO, PATCH,
  AUTOMAÇÃO e ADMINISTRAÇÃO (este só para admin). A barra superior traz o botão de recolher,
  a trilha (grupo › página), as versões do servidor e do agente publicado e o menu da conta,
  com o Sair.
- Contadores no menu: pedidos do autoatendimento esperando a TI (Autoatendimento), campanhas
  pausadas (Campanhas) e máquinas com a fonte de busca falhando, sem o catálogo offline
  (Visão geral). A conta é refeita a cada 20 s, e na hora depois de decidir um pedido ou
  mexer numa campanha.
- Sem JavaScript: o recolher é um checkbox. Na tela larga, recolhe para só os ícones; em tela
  estreita (até 960 px), a barra vira gaveta, que fecha tocando fora. O estado não é guardado
  entre páginas.
- Logotipo: Configuração › Sistema › Aparência. PNG ou JPEG, até 256 KB e 1024×1024 pixels,
  conferido pelo conteúdo (assinatura e decodificação completa), nunca pelo nome; fica no
  banco (`panel_assets`, migração 37) e vale para todos os servidores no mesmo banco (os
  outros veem a troca em até 1 minuto). Aparece no menu e na tela de login, servido em
  `/painel/logo` com `nosniff`, CSP `sandbox` e cache pela versão do arquivo. Trocar e remover
  vão para a auditoria (`aparencia.logo`, `aparencia.logo_remover`). Sem logotipo, o ícone
  padrão. O patchd não traz o logotipo de nenhuma instituição: quem instala envia o seu.

### A Central de software no mesmo layout (Aula 19.1b)

- A Central usa o layout do painel (`internal/ui`, compartilhado): barra lateral com o
  logotipo, o nome "Central de software" e o nome do painel, o usuário (conta e domínio,
  com as iniciais) e as seções SOFTWARE (Atualizações, Loja, Meus pedidos) e COMPUTADOR
  (Reinício); barra superior com a hora da consulta, o botão Atualizar e a versão do agente.
- O logotipo é o mesmo do painel: a loja (`/api/v1/agent/selfservice/catalog`) leva o nome
  do painel e o SHA-256 do logotipo; o agente baixa o arquivo por
  `/api/v1/agent/selfservice/logo` (credencial da máquina) só quando o SHA-256 muda, confere
  o SHA-256 e a imagem e entrega à Central pelo canal local (operação `logo`). Falhou, fica
  o anterior (ou o ícone padrão) e o mesmo arquivo só é tentado de novo depois de 10 minutos.
- Exige a release do agente com a 19.1b; com servidor até a 19.1, a Central fica com o
  ícone padrão.
- **Atualizar tudo** (com 2 ou mais atualizações para pedir): as que passam pela TI viram
  pedidos à TI na hora; as outras entram numa fila da própria Central, que pede uma de cada
  vez (o servidor só aceita uma instalação por máquina) e mostra "Instalando" e "Na fila".
  Cada pedido é do usuário, pelo canal local, com a loja refeita pelo servidor. "Cancelar o
  restante" tira da fila o que ainda não foi pedido. A fila vive enquanto a Central estiver
  aberta; fechar a Central ou sair da sessão para a fila (o que já foi pedido continua).

## Parâmetros pela tela (Aula 19.2)

- Configuração › Parâmetros (só admin) muda, sem reiniciar o servidor: nome do painel e fuso
  horário (na hora), os três prazos da retenção (na próxima passada, em até 1 hora: quem
  encurtou por engano tem tempo de voltar atrás), o intervalo da sincronização do catálogo
  (recalculado na hora; "desligada" para o agendamento sem parar o servidor) e a URL do cache
  de repositórios (no próximo check-in; travada sem `PATCHD_REPO_CACHE_DIR`).
- A tela vence: o valor gravado (tabela `settings`, migração 38) vale no lugar do .env. O
  .env é o valor inicial e o que volta no **restaurar o padrão**. Cada valor é conferido com
  a mesma regra da partida; um cartão grava tudo ou nada.
- Auditoria: `parametro.alterar` (antes → depois) e `parametro.restaurar`; com as notificações
  ligadas, um e-mail "Parâmetros alterados" para a lista.
- Vários servidores no mesmo banco: cada um relê a tabela a cada 30 segundos. Uma linha
  inválida (mexida à mão, de outra versão) é ignorada com aviso no log, e o .env vale.
- Terminal: `patchd-server settings list` (o que vale, de onde vem e o do .env) e
  `patchd-server settings reset PARAMETRO|-all` (a saída de emergência, na auditoria como
  `cli.settings.reset`). `patchd-server retention` usa os prazos da tela.
- Ficam sempre no ambiente: banco, portas, TLS, chaves e segredos, pastas, a URL pública e os
  endereços do AD e do RHBK. Grupos de acesso: Aula 19.4.

### Notificações pela tela (Aula 19.3)

- O cartão Notificações muda servidor SMTP (vazio desliga), segurança (STARTTLS, TLS direto
  ou sem TLS), usuário, remetente, destinatários e os avisos (caixas: pedidos, campanhas,
  fontes). Ficam no ambiente: a senha (`PATCHD_SMTP_PASSWORD_FILE`, o segredo `smtp_password`)
  e a CA (`PATCHD_SMTP_CA_FILE`).
- O conjunto é conferido antes de gravar, como na partida: com usuário, o arquivo da senha
  tem de ter a senha; sem TLS, nada de usuário; ao menos um destinatário. Mudança só em
  outro cartão não confere o SMTP.
- Vale na hora: o servidor remonta o envio quando a configuração muda (também a senha
  trocada no arquivo), sem reiniciar. O laço dos avisos roda sempre e pula a passada enquanto
  estiverem desligadas. "Mandar e-mail de teste" também fica no cartão (usa o que está
  gravado). `patchd-server notify status|test|pending` usam o mesmo que o servidor.

### Grupos de acesso pela tela (Aula 19.4)

- Cartão Acesso: grupos admin do AD além do garantido, grupos de leitura do AD, papéis admin
  do RHBK além do garantido e papéis de leitura do RHBK (sAMAccountName ou o valor do claim,
  separados por vírgula). Travados com o AD ou o RHBK desligados.
- O garantido fica no .env e é sempre admin: `PATCHD_LDAP_ADMIN_GROUP` e
  `PATCHD_OIDC_ADMIN_ROLE`. A tela não tira; assim sempre existe um caminho de admin (e a
  conta local de contingência, para o AD fora do ar).
- Conferências: um grupo não pode estar nas duas listas; o AD não pode ficar sem grupo
  nenhum; no máximo 20 por lista. Um grupo da tela que o AD não acha fica de fora (o login
  segue, com aviso no teste de Acesso); o do .env que não existe torna o AD indisponível,
  como antes.
- Vale no próximo login. Tirar um grupo (ou papel), inclusive pelo "restaurar o padrão",
  encerra as sessões abertas por aquela origem, menos a de quem mudou: quem saiu da lista
  não fica com o perfil até a sessão vencer. Na auditoria: `parametro.sessoes`.
- O teste do AD (Configuração › Acesso) mostra por qual grupo o usuário entrou e os grupos
  da tela que o AD não achou; `patchd-server ad check` mostra só as quantidades.

## Troca das chaves de jobs e de release (Aula 20.7)

O agente aceita duas chaves de cada tipo: a atual e a **seguinte**, as duas embutidas no build
(`PATCHD_JOB_PUBKEY` e `PATCHD_JOB_PUBKEY_NEXT`; `PATCHD_UPDATE_PUBKEY` e
`PATCHD_UPDATE_PUBKEY_NEXT`). A troca vira três passos, sem máquina parada e sem reinstalar:
embutir a seguinte, esperar a frota, passar a assinar com ela. O agente informa no estado as
impressões das chaves que aceita, e Configuração › Sistema › **Chaves na frota** conta quantas
máquinas (com estado nos últimos 30 dias) aceitam cada uma; agentes anteriores à 20.7 aparecem
à parte.

Regras do `scripts/build.sh`: as quatro chaves têm de ser diferentes entre si; as seguintes
são opcionais. O `patchd-release sign` recusa assinar executáveis que não tragam a chave de
quem assina (como atual ou como seguinte): a frota instalaria a versão, mas recusaria a
próxima, e a troca pararia no meio. O `keygen`, o `pubkey` e o `sign` mostram a impressão da
chave.

**Chave seguinte de release, sempre pronta (F5).** Gere já a próxima, guarde a privada só
offline (dois pendrives cifrados, em lugares diferentes) e embuta a pública como
`PATCHD_UPDATE_PUBKEY_NEXT`. Se a atual se perder ou vazar, a próxima versão sai assinada pela
seguinte, que a frota já aceita.

Roteiro da chave de **release**:

1. `patchd-release keygen -key <pendrive>/release-N.key` gera a seguinte; a pública vai para
   `PATCHD_UPDATE_PUBKEY_NEXT` (junto das outras, fora do repositório).
2. Build e publicação de uma versão **assinada pela atual**, com a seguinte embutida.
3. Esperar "release <seguinte>: N de N" em Chaves na frota.
4. A seguinte vira a atual: `PATCHD_UPDATE_PUBKEY` = a dela; `PATCHD_UPDATE_PUBKEY_NEXT` = uma
   nova (passo 1 de novo). Build e publicação **assinada pela nova atual**.
5. A chave antiga sai do uso: os agentes que atualizaram no passo 4 já não a aceitam. Destrua
   as cópias só depois de "release <antiga>" sumir de Chaves na frota.

Roteiro da chave de **jobs**:

1. `patchd-server job keygen -out <arquivo>` gera a seguinte (o arquivo fica offline até o
   passo 3); a pública vai para `PATCHD_JOB_PUBKEY_NEXT`.
2. Build e publicação de uma versão com a seguinte embutida. Esperar "jobs <seguinte>: N" igual
   ao total em Chaves na frota.
3. Trocar o conteúdo do segredo `job_signing_key` (o arquivo de `PATCHD_JOB_SIGNING_KEY_FILE`)
   pela seguinte e reiniciar o servidor. A linha passa a mostrar a nova como "a do servidor";
   em atenção (amarelo), alguma máquina ainda não a aceita, e os jobs para ela seriam
   recusados ("assinatura inválida"). Os jobs já criados com a antiga continuam valendo.
4. Próximo build: `PATCHD_JOB_PUBKEY` = a nova, `PATCHD_JOB_PUBKEY_NEXT` vazia (ou já a
   próxima). Depois que a frota atualizar, os jobs antigos que restarem na fila vencem sozinhos.

Ainda não giram por este roteiro: a credencial de cada máquina (aposentar e registrar de novo
continua sendo o caminho) e a aprovação de instaladores por uma segunda chave (seção 5 de
`docs/seguranca.md`).

## Cópias do banco no painel (Aula 20.8)

Configuração › Sistema › **Cópias do banco** mostra a última cópia registrada pelo
`backup.sh` (há quanto tempo, cifrada ou não) e o último ensaio do `restaurar.sh --testar`
(conferiu ou não). O ensaio agora grava o resultado na tabela `restore_tests` do banco em uso
(migration 0040); os dados do banco em uso continuam intocados. Vermelho: nenhuma cópia, ou o
último ensaio não conferiu. Amarelo: cópia com mais de 36 h (o timer parou?), sem cifra, sem
ensaio, ou ensaio com mais de 35 dias. Cópia fora do host o servidor não enxerga: roteiro de F4
em `docs/seguranca.md` (seção 5f).

## Pacote para revisão externa (Aula 20.8)

```bash
./scripts/build.sh v0.17.11            # o build da versão, com a árvore limpa
./scripts/revisao.sh v0.17.11          # revisao/patchd-revisao-v0.17.11.tar.gz (+ .sha256)
```

O pacote tem o código do commit, o SBOM de cada executável (CycloneDX 1.5), os resultados do
`seguranca.sh`, do `go test` e do `fuzz.sh`, o `docs/seguranca.md` e o `docs/rotas.md`, e um
LEIAME para quem revisa. Pule etapas com `PULAR="seguranca testes fuzz"`; `FUZZTIME` muda o
tempo por alvo (padrão 30s); `ZAP=zap/relatorios/AAAAMMDD-HHMM-modo` inclui um relatório do
ZAP (encerre antes a sessão da varredura: `patchd-server session end`).

A lista de termos que nunca podem sair fica fora do repositório, em
`~/.config/patchd/proibidos.txt` (ou `PATCHD_PROIBIDOS`), permissão 600:

```text
# um termo por linha; maiúsculas e minúsculas tanto faz
<domínio interno>
<prefixo dos IPs internos, com o ponto final>
<nome de host do servidor>
env:PATCHD_PUBLIC_URL       # o valor desta variável no .env ou no deploy/.env
```

Além da lista, o script procura o conteúdo de cada arquivo de `deploy/secrets/`, o cookie da
varredura do ZAP e chaves privadas (do patchd, do age e PEM). Um achado mostra o arquivo e a
linha da lista (nunca o valor) e o `.tar.gz` não é gerado. O histórico do git é só contado.

## Nome da Central de software (Aulas 20.9 e 20.10)

Os avisos da Central (o balão perto do relógio) mostram no título o nome do programa que o
Windows tira da "Descrição do arquivo" do executável. Sem ela, aparecia o nome do arquivo
(`patchd-app.exe`). Agora:

- **O nome é um parâmetro do painel**: Configuração › Parâmetros › Aparência › **Nome da
  Central de software** (`PATCHD_APP_NAME`, padrão "Central de software", de 1 a 40
  caracteres). Vale no título dos avisos, no ícone perto do relógio (dica e menu) e no topo da
  página da Central.
- **Como chega às máquinas**: a loja que a Central pede ao agente traz o nome; o agente guarda
  (`central-nome.txt` na pasta de dados) e grava na descrição da cópia `patchd-app.exe`. A
  Central percebe a cópia trocada e reabre sozinha em até 1 minuto, já com o nome. A página
  mostra o nome novo na hora; o título dos avisos, depois do próximo logon do usuário (o
  Windows guarda o nome de quem manda os avisos).
- **No build**: o `scripts/build.sh` grava o recurso de versão (versão, `ProductName` patchd,
  nome original `patchd-agent.exe` e a descrição padrão num espaço de tamanho fixo, que o
  agente troca sem mexer no resto do arquivo). `PATCHD_APP_COMPANY` (opcional) vai em
  Propriedades › Detalhes › Empresa. Build sem o `build.sh` (sem o recurso): a Central funciona
  e o título volta a ser o nome do arquivo.

O serviço (`patchd-agent.exe`) fica com a descrição padrão; só a cópia da Central leva o nome do
painel.

## Firmware no inventário (Aula 21.1)

O agente informa o estado do firmware no inventário, **só leitura**, e a aba Hardware da
máquina mostra o cartão **Firmware**:

| Linha | De onde vem (Windows) | Linux |
|---|---|---|
| Modo (UEFI ou BIOS legado) | `GetFirmwareType` | `/sys/firmware/efi` |
| Secure Boot | `HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State` | variável EFI `SecureBoot` |
| TPM (2.0, 1.2, nenhum) | WMI `root\cimv2\Security\MicrosoftTpm`, `Win32_Tpm` | `/sys/class/tpm/tpm0` |
| Senha de administrador e de ligar (definida ou não) | WMI do fabricante: HP `HP_BIOSPassword`, Dell `PasswordObject`, Lenovo `Lenovo_BiosPasswordSettings` | — |

- Das senhas, só se estão definidas: nada da senha passa pelo agente.
- Modelo sem a interface do fabricante (antigo, ou sem o WMI da BIOS): "sem informação", sem
  erro no inventário. No macOS, o firmware não é coletado.
- Destaques: senha de administrador não definida em vermelho; BIOS legado, Secure Boot
  desligado, TPM 1.2 ou nenhum em amarelo.
- O servidor confere os valores (conjunto fechado); um firmware fora dele é descartado, o resto
  do inventário entra, e o motivo fica nos erros do relatório.

### Firmware na frota (Aula 21.2)

Inventário › Equipamentos ganhou uma segunda linha de cartões, contando a frota inteira:

| Cartão | Conta as máquinas com |
|---|---|
| BIOS sem senha | senha de administrador da BIOS **não definida** (vermelho) |
| Secure Boot desligado | Secure Boot desligado, inclusive as em BIOS legado |
| TPM ausente ou 1.2 | sem TPM ou com TPM 1.2 (o Windows 11 pede o 2.0) |
| Senha sem informação | a senha não foi informada: agente anterior à 21.1, modelo sem a interface do fabricante, Linux e macOS |

Clicar num cartão filtra a lista (`?firmware=sem-senha`, `secure-boot-desligado`, `tpm` ou
`sem-info`), junto com a busca e o status; clicar de novo tira o filtro. A coluna **Senha da
BIOS** mostra definida, não definida (em vermelho) ou —, e ordena. O CSV leva o mesmo filtro e
as colunas `firmware_modo`, `secure_boot`, `tpm`, `senha_admin_bios` (sim, nao ou vazio) e
`senha_bios_fonte`.

O firmware fica em colunas do inventário (migration 0041), como o fabricante e o modelo desde a
0025: a lista não relê o relatório inteiro de cada máquina. A migration preenche as colunas do
inventário atual de cada máquina.

## Guarda da senha da BIOS (Aula 21.4)

O substituto do `.bat` de senha da BIOS começa pela guarda: o servidor sorteia a senha de
cada máquina e guarda só o envelope cifrado. A gravação na BIOS é a Aula 21.5; o desenho e as
decisões estão em `docs/bios-senha.md`.

**Ligar (uma vez).** No host, com o repositório:

```bash
./deploy/gerar-segredos.sh                                   # cria deploy/secrets/bios_escrow_key vazio
go run ./cmd/server bios keygen -out deploy/secrets/bios_escrow_key
sudo setfacl -m u:65532:r deploy/secrets/bios_escrow_key     # o servidor (UID 65532) lê
```

O `keygen` imprime a impressão da chave (12 hexadecimais). No `deploy/.env`,
`PATCHD_BIOS_ESCROW_KEY_FILE=/run/secrets/bios_escrow_key`, e suba de novo. O log da partida
diz "guarda da senha da BIOS ligada" com a mesma impressão, e Configuração › Sistema mostra
"Guarda da senha da BIOS: ligada". `patchd-server bios key` mostra a impressão da chave
configurada.

**Cópias da chave.** Sem a chave privada, nenhuma senha guardada pode ser revelada, e a saída
passa a ser a assistência do fabricante. Guarde duas cópias fora do servidor (como a chave de
release, Aula 20.7), **longe da cópia do banco**: o banco tem os envelopes; banco e chave juntos
abrem todas as senhas.

**Usar.** Na máquina, aba Hardware, cartão "Senha da BIOS":

| Ação | Quem | O que acontece |
|---|---|---|
| Preparar senha nova | admin | sorteia 12 caracteres (minúsculas e números, sem ambíguos) e guarda o envelope, como `pendente`. Nada muda na máquina |
| Revelar | admin, com motivo (10 a 200 caracteres) | a auditoria (`bios.senha.revelar`, com o motivo) é gravada antes; a senha aparece numa página fora do cache, em grupos de 4 (digite sem os espaços); a lista de notificações recebe um e-mail, sem a senha |

O perfil leitura vê o cartão (estados, quem sorteou, quantas revelações), sem os botões. As
senhas guardadas não entram na retenção: valem enquanto a placa existir, inclusive em máquina
aposentada.

### Gravar a senha na BIOS (Aula 21.5)

Só HP, por enquanto, com o agente v0.18.4 ou mais novo (Windows). Antes da primeira gravação
numa máquina, confira no inventário (`patchd-agent -inventory`, `hardware.firmware`) que
`interface` é `hp` e `sure_admin` é `False`.

1. Na aba Hardware da máquina, **Preparar senha nova** (se ainda não houver uma pendente).
2. **Gravar na BIOS**. Na primeira troca (a BIOS tem a senha da frota e a guarda não), o
   formulário pede a senha atual: digite a da frota. Ela vai cifrada para a máquina e não fica
   guardada.
3. A máquina grava no próximo check-in (até 1 h; `Restart-Service patchd-agent` antecipa).
   O cartão mostra a resposta: **ativa**, **falhou** (com o código da HP), **incerta**, ou de
   volta a pendente com o motivo da recusa.
4. Confira na frente da máquina: entre na BIOS com a senha revelada (motivo: "conferência da
   gravação").

**Código 6** (acesso negado): a senha atual não confere. Não repita o pedido às cegas: a BIOS
da HP pode travar novas tentativas até reiniciar. Confirme a senha da frota antes.

**Incerta**: nada mais é gravado nessa máquina até alguém resolver. Revele a nova e a anterior,
teste qual a BIOS aceita e clique em **A nova vale** ou **A anterior vale**.

## Inventário avulso da frota (Aula 22.1)

Para a frota que ainda não fala com o servidor (sem o agente instalado, ou sem rota até ele),
o mesmo executável coleta o inventário só para leitura: `patchd-agent.exe -inventory` não
instala nada e não fala com ninguém. O roteiro da aula (`lab-coleta-frota.ps1`, fora do
repositório) roda isso pelo PsExec em uma lista de máquinas e grava um `.json` por máquina.

O resumo, sem banco nem servidor:

```bash
patchd-server inventory summary -dir PASTA            # só as contagens
patchd-server inventory summary -dir PASTA -maquinas  # e os nomes em cada contagem (fica com você)
```

Ele lê os três formatos que o Windows grava (UTF-16 do PowerShell 5, UTF-8 com e sem BOM) e
conta: interface da BIOS, senha de administrador, HP Sure Admin, Secure Boot, TPM e ligar
pela rede (pronta, bloqueada, sem informação, com os motivos). As regras são as mesmas do
painel (Aulas 21.2 e 22.1).

## Ligar pela rede: prontidão (Aula 22.1)

Na aba Hardware, o cartão **Ligar pela rede (Wake on LAN)** mostra a situação (pronta,
bloqueada ou sem informação), o que impede, os avisos, as configurações da BIOS que pesaram
(com o nome e o valor como o fabricante escreve) e as placas de rede físicas (cabo, Magic
Packet, permissão para ligar o computador). O mapa do módulo está em `docs/wake-on-lan.md`.

## Instaladores de terceiros pelo painel (Aula 23.3)

Em **Patch › Programas de terceiros › (programa)**, o cartão "Instaladores guardados" deixa o
admin fazer pela tela o que antes era só `patchd-server package fetch`:

- **Baixar**: a versão mais nova do winget-pkgs, ou a que for digitada (como o winget a
  escreve, ex.: `26.04`). É o mesmo caminho da linha de comando: o manifesto do winget-pkgs, o
  instalador do tipo da regra (x64), o SHA-256 conferido e o registro. **Tipo** (Aula 23.5):
  uma instalação MSI só é atualizada por um MSI (msi ou wix), e uma exe por um exe; quando a
  regra fixa um tipo (o 7-Zip, exe) e alguma máquina tem o outro, a "Atualização pelo patchd"
  da máquina diz "sem pacote … com o tipo MSI": escolha MSI no Baixar. A aprovação é da versão
  e vale para os instaladores dos dois tipos. O download roda no
  servidor, em segundo plano, **um por vez**; a página mostra o andamento e se atualiza
  sozinha até terminar (pode sair dela). Reiniciar o servidor interrompe o download; o que
  já foi registrado fica. O servidor precisa de saída para o GitHub e para o site do
  fabricante (no Podman, o `HTTPS_PROXY` do `deploy/.env`, docs/podman.md).
- **Excluir esta versão**: apaga os registros dos instaladores da versão e os arquivos da
  pasta `pacotes/` que nenhuma outra versão usa. Recusa a versão **aprovada** (revogue antes:
  a aprovação é uma decisão registrada), a que tem **job em aberto** com um dos instaladores
  (o agente ainda vai baixá-lo; cancele em Jobs ou espere) e a de **campanha não encerrada**.
  Excluir não muda nada nas máquinas; dá para baixar a mesma versão de novo depois.

Na auditoria: `terceiros.baixar` (com a versão pedida) e `terceiros.excluir`. Sem pasta de
conteúdo (`PATCHD_CONTENT_DIR`), o painel continua mostrando o comando da linha de comando.

## Localização do patrimônio (Aula 23.6)

**Configuração › Patrimônio** recebe o CSV que o sistema de patrimônio exporta, com o cabeçalho
`DEPREDIO,DESALA,UO,TOMBO,DEBEM,DESUBGRUPOBEM` (as colunas em qualquer ordem; só `TOMBO` é
obrigatória). Vale vírgula ou ponto e vírgula, UTF-8 com ou sem BOM, ou Windows-1252 (o "CSV" do
Excel no Windows). Até 20 MB.

- **Ligação pelo nome**: cada sequência de dígitos do nome da máquina, sem os zeros à esquerda,
  é comparada com o tombo, também sem os zeros. `FJ088594` é o tombo `88594`; o prefixo e o
  sufixo variam à vontade. Conta a sequência **inteira**: o tombo `1234` não liga `PC112345`.
- **Substituição**: cada envio troca a localização inteira, numa transação só. O tombo que saiu
  do arquivo some da tabela. Um arquivo sem nenhum tombo válido é recusado e não muda nada.
- **Tombo repetido** no arquivo: vale a última linha. Linhas sem tombo numérico ficam de fora; a
  tela mostra quantas e os números das primeiras.
- **Inventário**: as colunas Tombo e Local (prédio e sala; passe o mouse para ver UO, bem e
  subgrupo), o filtro de prédio, a busca por tombo, local ou descrição do bem, e as colunas
  `tombo,predio,sala,uo,bem,subgrupo_bem` no CSV.
- A aba lista as máquinas do inventário que não acharam tombo (o nome sem número, ou um número
  fora do arquivo) e as últimas importações. Cada envio fica na auditoria como
  `patrimonio.importar`; só o perfil admin envia.

## Descoberta de rede (Aula 23.7)

**Administração › Descoberta** acha máquinas na rede que ainda não têm o agente, para você
instalar nelas.

1. **Cadastre as faixas** de IP a varrer: CIDR (`10.0.0.0/24`) ou intervalo
   (`10.0.0.10-60`, ou `10.0.0.10-10.0.0.60`). Até 64 faixas; cada uma no máximo um `/16`.
   Dá para ligar e desligar uma faixa sem removê-la.
2. **Varrer agora** sonda os endereços das faixas ligadas. A sondagem é **TCP** (portas 445 e
   3389 do Windows, 5986 do WinRM HTTPS e 22 do SSH) feita **pelo servidor**, sem ping — o container roda sem root e
   não abre soquete bruto, e muita máquina ignora o ping mas atende essas portas. Só alcança
   as sub-redes que a VM do servidor enxerga. A página se atualiza sozinha enquanto varre;
   cada varredura sonda no máximo 16 384 endereços (o resto fica para a próxima, marcada
   "parcial").
3. **Sem agente**: o host vivo é cruzado com o inventário pelo **IP** (os endereços que o
   agente reporta e o do check-in) e pelo **nome** (o reverse-DNS do host contra o hostname do
   inventário). O que casa some da lista; o que sobra é o que não tem agente. O palpite de SO
   sai das portas (445/3389/5986 → Windows; 22 → Linux/SSH).
4. **Configuração › Descoberta e instalação Windows** permite cadastrar uma conta
   administrativa e habilitar **Instalar automaticamente após a varredura**. Ao terminar a
   varredura, o servidor instala nas estações Windows sem agente das faixas ainda habilitadas.
   Máquinas já conhecidas no inventário e hosts Linux/SSH ficam de fora.
5. Para cada host, **Instalar** também abre o comando manual conforme o SO: **PowerShell remoto
   (WinRM)**, **PsExec** e **GPO** para Windows, **SSH** para Linux/macOS. O IP e o endereço
   do servidor vão preenchidos; o usuário administrador e o token ficam a preencher. Crie um token em Configuração › Agente e salve o
   `patchd-token.txt` junto do instalador.

A cada varredura os hosts são regravados por inteiro (uma foto da rede). As faixas e as
últimas 50 varreduras ficam. Cada varredura e cada mudança de faixa entram na auditoria
(`descoberta.varrer`, `descoberta.faixa`); tudo é do perfil admin.

### Preparar a instalação automática Windows

1. Gere uma chave exclusiva para cifrar a credencial:
   `patchd-server discovery keygen -out /caminho/seguro/discovery.key`.
   O arquivo é criado com permissão 0600 e nunca sobrescrito. Configure
   `PATCHD_DISCOVERY_KEY_FILE=/caminho/seguro/discovery.key` no processo do servidor.
   Guarde a chave fora do banco e inclua-a no plano de backup; sem ela, cadastre a senha
   novamente. Não reutilize a chave de jobs ou da guarda da BIOS.
   No Compose de `deploy/`, configure no `.env`
   `PATCHD_DISCOVERY_KEY_HOST_FILE=/caminho/absoluto/discovery.key` e
   `PATCHD_DISCOVERY_KEY_FILE=/run/patchd/discovery.key`. Dê leitura do arquivo ao UID
   do servidor, como para os demais segredos do deploy, e recrie o serviço.
2. Publique uma versão contendo o agente **Windows amd64** em Configuração › Agente.
   O executável é conferido pelo tamanho e SHA-256 antes do envio e novamente na estação.
   Configure `PATCHD_PUBLIC_URL` com a URL HTTPS que as estações alcançam.
3. Nas estações, habilite **WinRM HTTPS na porta 5986**, com certificado cujo SAN contenha
   o IP varrido e cuja CA seja confiável no servidor. Libere o firewall apenas para o
   servidor patchd. A conta cadastrada deve poder abrir a sessão WinRM e instalar serviços
   como administrador local; permissões de domínio, restrições de UAC remoto e políticas
   locais continuam sendo respeitadas. Só cadastrar a senha não habilita WinRM.
4. Cadastre usuário e senha em Configuração › Descoberta e habilite a instalação automática.
   Senha vazia mantém a anterior; trocar de usuário exige informar a senha.
   A senha é cifrada com HPKE antes de ir ao banco, não volta ao HTML e não entra na auditoria.
5. Cadastre as faixas e clique em **Varrer agora**. A instalação acontece automaticamente
   ao final dessa varredura; isso não agenda varreduras periódicas. O histórico da página
   mostra o último resultado por IP e a auditoria registra `descoberta.credencial` e
   `descoberta.instalar`.

A transferência usa NTLM sobre HTTPS com verificação de certificado, sem opções para ignorar
TLS. O binário, a CA do servidor e o token seguem pelo stdin cifrado da sessão WinRM.
O token é de uso único, vence em dez minutos e é revogado ao terminar a tentativa.
Arquivos temporários na estação têm ACL restrita a SYSTEM e Administradores e são removidos
no encerramento normal da sessão. Uma queda da conexão ou da estação pode impedir a limpeza;
o token continua sujeito à validade.

O serviço deve estar em execução para o resultado virar **aguardando registro**.
Isso não confirma inventário: a próxima varredura cruza o registro recebido e passa a mostrar
**agente identificado no inventário**. Falhas não interrompem as outras estações.
Tentativas concorrentes para o mesmo IP são bloqueadas no banco; novas tentativas aguardam
15 minutos após falhas ou 24 horas enquanto aguardam registro. A instalação tem limite
de três minutos por estação e trinta minutos por lote; alvos ainda não tentados ficam para
uma nova varredura. Desabilitar a instalação ou uma faixa interrompe novos envios; a tentativa
em andamento pode terminar. Windows ARM64 e instalações automáticas por SMB/PsExec não fazem
parte deste fluxo.

Um aviso: a varredura abre conexões TCP para toda a faixa. Avise a equipe de rede antes de
varrer sub-redes grandes, para a sondagem não ser confundida com um ataque pelo IDS/IPS.

## Execução de scripts remotos (Aula 23.8)

**Automação › Scripts** executa, nas máquinas da frota, um conjunto de arquivos que você sobe
no painel — como as automações do ITOM.

1. **Suba o bundle**: um ZIP com o que a execução precisa — o executável, o `.ini` e o script
   que começa. O script de entrada pode ser `.ps1`, `.bat`, `.cmd` ou `.exe` (Windows) ou
   `.sh` (Linux e macOS). Até 256 MiB; o servidor guarda o ZIP em
   `$PATCHD_CONTENT_DIR/bundles/<sha256>.zip`.
2. **Escolha por onde começa**: o seletor lista só os arquivos executáveis do ZIP. O script
   roda **na pasta onde o bundle foi extraído**, com os outros arquivos ao lado — ele os
   chama por caminho relativo (`.\agente.exe -c config.ini`, `./apoio.sh`).
3. **Argumentos e prazo**: argumentos opcionais, separados por espaço, pelas mesmas regras do
   job de terceiros (sem aspas, espaços dentro do token, caminhos ou barras invertidas). O
   prazo vai de 30 a 3600 s; passando dele, o script é interrompido e o job falha.
4. **Marque as máquinas** e execute. Sai um job por máquina; o script roda no próximo
   check-in de cada uma. Acompanhe em **Automação › Jobs**: o resultado traz o código de
   saída e a saída do script (as últimas ~4000 letras).

O bundle só aparece para as máquinas do sistema dele: um ZIP só com `.ps1` não é oferecido a
Linux, e vice-versa. O agente confere de novo na máquina e recusa o que não for do sistema.

### O que protege a execução

- **O job não leva linha de comando.** Leva o SHA-256 do ZIP, o nome do arquivo de dentro
  dele por onde começar, e os argumentos conferidos — tudo dentro do payload assinado pela
  chave de jobs. O agente monta o comando e passa o arquivo e os argumentos como `argv`,
  nunca por um shell. Não há como apontar o ponto de entrada para `C:\Windows\System32`.
- **Nada roda antes de o hash conferir.** O agente baixa o ZIP, compara tamanho e SHA-256 com
  o que veio no job assinado, e só então extrai. Hash diferente: o job é *rejeitado*, e nada
  foi tocado na máquina.
- **A extração é conferida arquivo por arquivo** (Zip Slip): nome relativo, sem `..`, sem
  barra invertida nem unidade, no máximo 4 pastas de fundura, só arquivo comum — link
  simbólico e dispositivo não entram. Há teto de quantidade, de tamanho descompactado e de
  taxa de compressão (zip bomb).
- **O script roda com a conta do agente**: SYSTEM no Windows, root no Linux. É o que torna a
  coisa útil e perigosa ao mesmo tempo — qualquer admin do painel pode rodar o que quiser na
  frota. Tudo fica na auditoria (`script.bundle`, `script.executar`), e o bundle, o ponto de
  entrada e as máquinas aparecem lá.
- **Nada fica na máquina**: o ZIP e a pasta extraída são apagados quando o job termina.

Um detalhe que custa caro quando falta: se o script deixar um processo em segundo plano, ele
segura o cano da saída. O agente mata o processo no fim do prazo e espera no máximo mais 5 s
pela saída — sem isso, o job ficaria preso até o processo em segundo plano morrer.
