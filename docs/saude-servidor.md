# Saúde do servidor

Pedido de 03/11/2026: um painel para saber a saúde do próprio servidor do patchd e **o que
aumentar** (CPU, memória, banco, disco) quando o parque cresce. Fica em **Configuração ›
Saúde**, só para o admin, e funciona sem o Prometheus (as métricas da Aula 10.2 continuam
como estão, para quem já tem um).

## 1. Decisões

| | Decisão | Consequência |
|---|---|---|
| S1 | **O servidor mede a si mesmo** a cada minuto, sem agente externo | `internal/srvhealth`: só a biblioteca padrão e `golang.org/x/sys`. No Linux, `/proc` e o cgroup v2 (dentro do container vale o limite do container); fora do Linux, só o runtime do Go |
| S2 | **24 horas por minuto na memória + 90 dias por hora no banco** | Os minutos não vão para o banco (seriam mais escrita no que se quer medir). Reiniciar o servidor zera as 24 horas; a hora fechada fica em `server_health_hourly` (migração 0054) |
| S3 | **Recomendações por regra fixa**, sobre o **p95** das 24 horas | Um pico isolado (o catálogo de madrugada) não pede hardware; o que acontece em 5% dos minutos, sim. Os limites ficam no código (`advise.go`): são regras de dimensionamento, não preferência de tela |
| S4 | **Downloads fora do tempo de resposta** | Catálogo offline, pacotes, versões do agente e os arquivos da aba Agente levam o tempo da rede de quem baixa; contam no volume, não no p95 |
| S5 | **Sem segredo na tela** | Nada de URL do banco nem de usuário; os números do PostgreSQL vêm de `pg_stat_database` e `pg_stat_activity`, visíveis a qualquer papel (sem superusuário) |

## 2. O que é medido

| Medida | De onde | Observação |
|---|---|---|
| CPU do patchd | `getrusage` | em % da capacidade: os núcleos, ou a cota `cpu.max` do cgroup se menor |
| CPU da máquina | `/proc/stat` | a máquina inteira (o PostgreSQL também, se estiver nela) |
| Memória do patchd | `memory.current` / `memory.max` do cgroup; sem limite, o RSS contra `MemTotal` | é o limite que mata o processo |
| Memória da máquina | `/proc/meminfo` (`MemAvailable`) | |
| Carga | `/proc/loadavg` | |
| Go | `runtime.MemStats` | heap, goroutines, pausas do GC no minuto |
| Requisições | o registro de acesso (a mesma rota do ServeMux das métricas) | por minuto: volume, das máquinas, lentas (> 1 s), 5xx, média e p95 (histograma de baldes); por rota, desde a partida. Os downloads de arquivo e o **canal em tempo real** (que o servidor segura até 45 s de propósito) contam no volume, não no tempo |
| Pool do banco | `pgxpool.Stat`, olhado a cada 5 s | pico de conexões em uso no minuto, conexões pedidas e as que esperaram pool vazio |
| PostgreSQL | `pg_database_size`, `pg_stat_database`, `pg_stat_activity` | tamanho, conexões contra `max_connections`, leituras da memória (cache), arquivos temporários, deadlocks, a consulta ativa mais longa, as maiores tabelas |
| Disco | `statfs` das pastas de dados | `PATCHD_CONTENT_DIR`, `PATCHD_REPO_CACHE_DIR`, `PATCHD_RELEASES_DIR`; o mesmo volume conta uma vez |

## 3. As regras

Com menos de 30 minutos medidos (o servidor acabou de subir), as regras das 24 horas esperam;
disco e banco já valem.

| Recurso | Atenção | Aumentar | O que a tela manda fazer |
|---|---|---|---|
| CPU do patchd | p95 ≥ 70% | p95 ≥ 85% | aumentar a CPU (ou a cota `cpus:` do container, se ela é menor que a máquina) |
| CPU da máquina, com o patchd folgado | p95 ≥ 75% | p95 ≥ 90% | é outro processo (em geral o PostgreSQL): mais CPU na máquina, ou o banco em outra |
| Memória do patchd | p95 ≥ 80% do limite | p95 ≥ 90% | aumentar `mem_limit:` do container (ou a memória da máquina) para 1,5× o pico |
| Memória da máquina | p95 ≥ 90% | pico ou p95 ≥ 95% | aumentar a memória da máquina (o cache do banco usa a mesma) |
| Pool do banco | ≥ 5% das conexões esperaram, ou o pool cheio em ≥ 2% dos minutos | ≥ 20% esperaram, ou ≥ 5% com espera média ≥ 100 ms | aumentar `pool_max_conns` na `PATCHD_DATABASE_URL` (o dobro), sem passar do `max_connections`; se o banco está sem CPU, CPU antes |
| Conexões no PostgreSQL | ≥ 80% do `max_connections` | ≥ 90% | aumentar `max_connections` (e a memória do banco) |
| Leituras da memória do banco (com ≥ 10.000 blocos lidos) | < 97% | < 90% | aumentar `shared_buffers` e a memória da máquina do banco |
| Disco livre (por volume) | < 20% | < 10% | aumentar o disco, ou liberar versões antigas e o cache |
| Disco: dias até encher no ritmo do banco (7 dias de crescimento) | < 90 dias | < 30 dias | aumentar o disco, ou encurtar a retenção |
| Tempo de resposta (p95 dos p95 por minuto) | ≥ 1 s | ≥ 3 s | aponta a causa: CPU, espera do banco, máquina ocupada, ou as rotas mais lentas |
| Erros 5xx (com ≥ 1.000 requisições) | ≥ 1% | — | não é falta de recurso: o log do servidor |

O veredito no alto junta os recursos: **Aumente agora** (algum crítico), **Fique de olho**
(alguma atenção) ou **Nada a aumentar agora**, com a folga da CPU ("aguenta cerca de 3× a
carga de hoje antes de chegar a 70%") e o crescimento do banco por dia, para planejar.

### Em que disco o banco cresce

A previsão "o disco enche em N dias" divide o crescimento do banco pelo espaço livre **do
volume onde o banco fica**. Nos containers (compose e Podman), o PostgreSQL mora nos volumes
nomeados, junto com o Conteúdo e o Cache de repositórios: a conta usa esse volume. As Versões
do agente são uma pasta do host (no Podman, em `/home/USUARIO/patchd/releases`), que pode
estar noutra partição: a tela mostra o espaço dela, mas o banco não a enche. Fora de container
(sem como saber onde o banco grava), a conta usa, por cautela, o disco mais cheio.

Para ver onde fica cada coisa no Podman: `podman info --format '{{.Store.GraphRoot}}'` (os
volumes, com o banco) e `df -h` dessa pasta e do `/home`.

### O que o próprio servidor faz para não pesar no pool

Antes de aumentar, confira se a versão já traz estes cuidados (desde 09/11/2026):

- **O que fica preso a uma sessão não usa o pool.** O `LISTEN` do canal em tempo real e as
  travas de sessão (o motor de campanhas, os e-mails, a sincronização do catálogo) usam uma
  conexão própria. Antes, o `LISTEN` tirava uma das 4 vagas para sempre, e cada trava prendia
  uma vaga enquanto o trabalho usava outra. Essas conexões contam no `max_connections` do
  PostgreSQL (até 4 a mais), não no `pool_max_conns`.
- **A avaliação do parque usa um quarto do pool** (1 conexão no pool padrão de 4; no máximo
  4), em vez de 4 em paralelo, que ocupavam o pool inteiro por quase um minuto a cada 5.
- **A avaliação guardada vale 1 hora** sem mudança (era 15 minutos). Inventário, busca ou
  catálogo novos refazem a máquina na hora, como antes.
- **As amostras de minuto do desempenho ficam 48 horas** (eram 7 dias, ~3,6 GB num parque de
  2.000 máquinas): só a curva de 24 horas e a montagem das horas as leem. O espaço apagado é
  reaproveitado pelo PostgreSQL (não volta para o disco), então o banco **para de crescer**
  em vez de encolher; o "cresce X por dia" da tela, que olha os últimos 7 dias, cai sozinho
  nos dias seguintes.
- **O motor de campanhas não traz o payload dos jobs** a cada minuto: o programa de cada job
  sai do payload no próprio banco.

Desde 11/11/2026:

- **O canal em tempo real saiu do tempo de resposta.** O agente deixa um pedido aberto e o
  servidor o segura até 45 s, até ter um job para ele; com o parque inteiro, esses pedidos eram
  quase todos os do minuto e punham o p95 perto de um minuto ("o servidor está lento"), sem
  nada estar lento. Eles seguem contando no volume de requisições.
- **No canal em tempo real, a credencial do agente fica um minuto em memória.** O agente
  refaz esse pedido a cada 45 s, e cada um consultava o banco só para saber de quem é a
  credencial: era a maior parte das conexões tiradas do pool. Agora vai ao banco uma vez por
  minuto por máquina. Os outros pedidos (check-in, relatórios, resultados, downloads) conferem
  no banco sempre. Aposentar, mesclar ou registrar pelo painel limpa na hora; pela linha de
  comando (outro processo), a aposentada ainda segura o canal por até um minuto, sem mandar
  nem receber nada pelas outras rotas.
- **A lista de pendências do parque é feita uma vez por minuto.** As telas de Patch (Sistema,
  Visão geral, Terceiros, os lotes) montavam a cada clique a lista de todas as pendências de
  todas as máquinas; com o Windows atrasado, são milhões de itens e mais de 1 GB por clique, e
  quem clicava de novo porque a tela "travou" começava outro cálculo por cima, até o servidor
  parar de responder. Agora há um cálculo por vez, quem chega no meio espera o mesmo, e a
  lista vale um minuto (aprovar e revogar aparecem na hora: as aprovações são lidas a cada
  pedido). A lista do Windows também deixou de criar um texto novo por CVE para a correção e
  o build instalado.
- **Limite brando de memória do Go.** Sem ele, o coletor do Go deixava o heap chegar ao dobro
  do que estava vivo antes de limpar, e o processo segurava essa memória, que numa máquina
  pequena falta ao PostgreSQL e ao cache de disco. O servidor define o limite em 40% da memória
  da máquina (75% do limite do container, se houver), com piso de 512 MB; o coletor trabalha
  mais perto dele (um pouco mais de CPU) e devolve a sobra ao sistema. `GOMEMLIMIT` no ambiente
  (inclusive `off`) vale no lugar do cálculo. A linha Memória do cartão Processo mostra o
  limite em uso.

Desde 13/11/2026, medido com um parque simulado de 4.000 máquinas Windows (230 programas
cada), 400 mil jobs, 300 mil eventos de auditoria, 4,5 milhões de amostras de desempenho e 90
dias de horas (banco de 2,4 GB):

| O que | Antes | Depois |
| --- | --- | --- |
| Avaliar o parque inteiro (partida, catálogo novo, de hora em hora) | 104 s | 9 s |
| Memória do processo com o parque avaliado | ~990 MB | ~110 MB (até ~240 MB com as telas de Patch em uso) |
| Montar as horas do desempenho, a cada hora | 19 s, 80 mil linhas regravadas | 1,8 s, 8 mil linhas |
| Início | 420 a 890 ms | ~110 ms |
| Página de uma máquina (qualquer aba) | 140 a 280 ms | 50 a 90 ms |
| Padrão da BIOS e senha em lote | 550 a 700 ms | ~80 ms |
| Parque | 1,8 MB de HTML numa tabela só | 66 KB por página |
| Visão geral do Patch (primeira abertura no minuto / as seguintes) | 1,2 s / 190 a 250 ms | 0,4 s / ~120 ms |

- **O catálogo é consultado uma vez por passada, não uma vez por máquina.** Os produtos do
  Windows, as CVEs do produto, as versões de referência dos terceiros e a situação das fontes
  são os mesmos para o parque inteiro; cada máquina refazia as mesmas consultas (com o
  catálogo real do MSRC, a das CVEs é a mais cara do sistema). A avaliação do Windows é feita
  uma vez por produto e build, e todas as máquinas com esse build apontam para a mesma lista
  de CVEs pendentes: um parque tem dezenas de builds, não milhares. As respostas valem
  enquanto o catálogo não sincroniza de novo, e no máximo 5 minutos.
- **A lista dos programas sem regra não fica na memória.** Ela é quase o inventário inteiro
  de cada máquina e só serve ao `patchd-server compliance -v`; guardada com a avaliação, era
  metade da memória do servidor.
- **O JIT do PostgreSQL fica desligado nas conexões do patchd.** O PostgreSQL compila as
  consultas de custo estimado alto; as listas do parque com uma subconsulta por máquina passam
  do limite na estimativa e levam dezenas de milissegundos na execução, e a compilação custava
  300 a 400 ms a cada chamada (a lista das fontes falhando, que alimenta o contador do menu a
  cada 20 s: de 437 ms para 23 ms). `jit=on` na `PATCHD_DATABASE_URL` liga de volta. Quem põe
  um PgBouncer na frente do banco precisa de `ignore_startup_parameters = jit` nele.
- **As horas do desempenho são montadas aos poucos.** A passada de hora em hora refazia as
  últimas 48 horas de todas as máquinas, para não perder a amostra que chega atrasada (o
  notebook que ficou fora da rede). Agora refaz as 3 últimas horas, e a amostra atrasada marca
  a hora dela (`perf_late`) para a passada seguinte. A primeira passada depois da partida
  ainda refaz as 48 horas, para cobrir o tempo em que o servidor ficou parado.
- **Índices para os jobs** (migração 71): por máquina (a aba Tarefas, a padronização a cada
  pedido do agente e a mesclagem varriam a tabela inteira), por término, por data com tipo e
  estado (as contagens do Início não visitam a tabela), e dois parciais (os pedidos do
  autoatendimento e o último job de configuração da BIOS de cada máquina).
- **O firmware numa coluna só dele** (`inventory_reports.fw_detail`): as telas de BIOS em lote
  liam o relatório inteiro de cada máquina, com a lista de programas, para tirar dele as
  configurações da BIOS.
- **O Parque em páginas de 100**, com o filtro e a busca nos links; o CSV traz todas.
- **A junção por item nas telas de Patch** resolve cada item uma vez por lista compartilhada
  e ordena o parque uma vez, em vez de ordenar as máquinas de cada item; o resumo "correções
  aguardando" conta as máquinas por posição, não por nome em mapa.

O que foi medido e ficou como está: o check-in (60 ms no p50 com 32 agentes ao mesmo tempo,
cerca de 500 por segundo: um parque de 4.000 máquinas com check-in a cada 5 minutos pede 13
por segundo); o inventário (15 ms), a busca (8 ms) e o registro (5 ms); a auditoria com filtro
(rápida quando o filtro acha eventos recentes; um filtro que não acha nada percorre a tabela,
~50 ms por 300 mil eventos).

A migração 71 levou cerca de 2 s nesse banco: cria os índices com a tabela de jobs travada
para escrita (1,1 s com 400 mil jobs) e copia o firmware do inventário atual de cada máquina
(1 s para 4.000). O servidor demora esse tanto a mais para subir na primeira vez.

## 4. A tela

- O veredito e as recomendações (o porquê, com os números, e o que fazer).
- Um medidor por recurso: CPU, memória, banco, disco e tempo de resposta, com a palavra do
  nível (ok, atenção, aumentar), não só a cor.
- Os gráficos (o mesmo desenho da aba Desempenho, em SVG, sem JavaScript): 24 horas por
  minuto; 7, 30 e 90 dias por hora, com o tamanho do banco e o disco livre.
- Onde o servidor gasta tempo: as rotas que mais somam tempo desde a partida.
- Banco (pool, conexões, cache, temporários, maiores tabelas), processo e as pastas de dados.

## 5. Fora desta versão

Alerta por e-mail da saúde do servidor, as consultas lentas do PostgreSQL
(`pg_stat_statements` exige extensão e privilégio), o disco do volume do banco quando ele está
em outra máquina (o patchd só mede o próprio disco) e limites configuráveis pela tela.
