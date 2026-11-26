# Implantação por prédio e andar

As campanhas novas do patchd são por **localização física**: o administrador escolhe prédios
inteiros e andares, opcionalmente um piloto (um prédio ou um andar), e cada equipamento recebe
o job na janela de manutenção do seu prédio. Os grupos de implantação numéricos (0 a 9) não
aparecem mais na criação de campanhas; continuam existindo só para as campanhas antigas, que
seguem acessíveis e funcionando como antes.

Não há limite de grupos: uma campanha pode ter qualquer quantidade de prédios.

## Visão geral

| Peça | Onde | O que faz |
|---|---|---|
| Prédios com ID estável | tabela `buildings` (migração 0085) | um ID por `DEPREDIO` do patrimônio |
| Andares | Configuração › Localizações | expressões regulares Go/RE2 por prédio (e as faixas antigas como alternativa) |
| Resolvedor de localização | `internal/api/locate.go` | prédio, andar e conflito de cada máquina; o mesmo para Shield, Localizações, Prédios e implantação, campanhas e o motor |
| Janelas por prédio | tabela `building_windows` | uma janela semanal por prédio, para todos os andares |
| Escopo e etapas | `campaigns.scope` (jsonb) e `campaign_targets` | o que foi escolhido, as etapas e os alvos gravados na criação |
| Motor | `internal/campaign` (`scope.go`, `stage.go`, `campaign.go`) | libera as etapas, respeitando a janela de cada prédio |

## Identificação dos prédios

O prédio vem do patrimônio importado em **Configuração › Patrimônio**: a máquina é ligada ao
bem pelo tombo no nome (os dígitos do hostname), como antes. Cada texto de `DEPREDIO` ganha um
**ID estável** na tabela `buildings`:

- nenhuma normalização junta dois textos: `FORUM GERAL` e `Forum Geral` são dois prédios (a
  tela mostra os nomes formatados, mas a chave é o texto exato);
- a cada importação, os nomes novos entram, os ausentes ficam marcados como **fora do
  patrimônio atual** (nada é apagado: a janela e o histórico continuam);
- **renomeação**: quando um prédio some e um nome novo aparece com os mesmos bens (todos os bens
  que continuam no patrimônio foram para o nome novo, são ao menos metade dos bens do antigo e ao
  menos 90% dos bens do novo, e nenhum outro prédio sumido cai no mesmo nome), o ID fica e só o
  nome muda — a janela, os andares e as campanhas seguem com ele; o nome anterior aparece na
  página. Qualquer dúvida (divisão de um prédio em dois, junção, poucos bens): nenhuma troca;
- prédios sem equipamentos ativos também aparecem (marcados "sem equipamentos").

Equipamentos sem bem do patrimônio ficam em **Sem localização identificada**: nunca são postos
num prédio arbitrário nem num grupo padrão.

## Andares por expressão regular

Em **Configuração › Localizações**, cada prédio tem as suas regras: o nome do andar e uma
expressão regular Go/RE2 para o IPv4 principal da máquina (ex.: `^10\.12\.1\.[0-9]+$`).

- Regras com o **mesmo nome** no mesmo prédio são o mesmo andar (várias redes ou VLANs).
- A regra vale **só no prédio dela**: a mesma expressão em outro prédio é independente, e um IP
  não põe a máquina no andar de outro prédio.
- Regras de **nomes diferentes** que casam o mesmo IP são **conflito**: a máquina fica sem andar
  (o prédio continua valendo). Ela entra em campanhas pelo prédio inteiro, nunca pelo andar nem
  no piloto por andar.
- Sem regex que case, vale a faixa de IP antiga dos grupos (a mais específica), com o nome
  cadastrado para aquele prédio; sem nome, a tela mostra "faixa 10.1.2.0/24".
- A expressão é validada antes de gravar (vazia, inválida ou que aceita texto vazio são
  recusadas).

As regras são compiladas uma vez por leitura (não por máquina), e o resolvedor é o mesmo em todas
as telas e no motor.

## Automação › Prédios e implantação

A página lista os prédios recolhidos, com o ícone e indicadores discretos. Cada prédio mostra:
equipamentos, andares identificados, equipamentos sem andar e em conflito, a janela, a próxima
abertura, a situação (ativa, desativada, sem janela) e os links para o inventário do prédio e
para as regras de andar. Há pesquisa por nome e filtro por "com janela ativa" ou "sem janela".

A seção "Grupos técnicos legados" saiu da página. As páginas de cada grupo antigo
(`/painel/grupos-implantacao/N`) continuam acessíveis pelas campanhas antigas.

### Janela de manutenção por prédio

Ao abrir um prédio, o administrador define dias da semana, início, término e fuso (o do painel
por padrão, ex.: `America/Porto_Velho`). Término antes do início atravessa a meia-noite
(22:00 a 04:00). A janela pode ser **desativada** (fica guardada, não vale para campanhas),
**reativada** ou **removida**. Vale para todos os andares do prédio; não há janela por andar
nesta etapa. Usa o pacote `internal/window` (as mesmas validações: de 10 min a 24 h).

### Janelas em lote

**Janelas em lote** lista os prédios (com a mesma pesquisa e filtro) com atalhos para selecionar
todos, os sem janela, ou limpar a seleção, e marca prédio a prédio. A mesma janela é aplicada
aos marcados numa transação. Se algum marcado já tem janela diferente (ou na remoção em lote), o
painel mostra antes quais configurações serão sobrescritas e pede confirmação.

### Janelas dos grupos antigos

Quando um prédio **sem janela** tem todas as máquinas (e o vínculo de prédio antigo, se houver)
em grupos com **a mesma janela**, a página oferece aplicar essa janela ao prédio. Prédios com
máquinas em grupos de janelas diferentes, ou parte em grupo sem janela, aparecem como pendentes
para configuração manual. O servidor recalcula a lista na hora de aplicar e nunca sobrescreve
uma janela de prédio. O grupo 0 não vira piloto permanente: o piloto é escolhido em cada campanha.

Toda alteração de janela (individual, em lote, legada, desativar, ativar, remover) vai para a
auditoria (`janela.predio`, `janela.lote`, `janela.predio-legada`...).

## Criar uma campanha

Em **Automação › Campanhas › Nova campanha** (e na campanha de remoção da página do programa):

**Escopo da campanha**

- **Todos os prédios identificados**, ou prédios marcados um a um (os prédios começam
  recolhidos; abrir mostra os andares, as contagens e a janela);
- **andares** específicos de um prédio (um ou vários);
- os **sem localização identificada**, só com a caixa de inclusão e a de confirmação.

O prédio inteiro inclui também os equipamentos sem andar e os em conflito; o andar, só os
identificados nele. Prédio e andares dele juntos não duplicam ninguém. A pesquisa acima do
formulário filtra a lista de prédios. O total de prédios e andares marcados aparece no próprio
formulário; o de equipamentos, na conferência (calculado no servidor).

**Implantação piloto**

- sem piloto;
- um prédio inteiro do escopo;
- um andar de um prédio do escopo.

O piloto precisa estar no escopo e ter equipamentos; no piloto por andar entram só os
identificados naquele andar (nunca os em conflito nem de outro andar). Os sem localização nunca
vão para o piloto. Um piloto que cobre o escopo inteiro é recusado.

Todos os tipos de campanha usam o mesmo escopo: busca de atualizações, Windows, Linux, programa
de terceiros, todos os programas aprovados, todas as atualizações do sistema, remoção de
programa e reinício.

### Conferência

A conferência mostra nome, tipo, programa ou atualização, total de equipamentos, as etapas
(piloto e o resto, com a previsão "Hoje" nos tipos que têm), os prédios do escopo com quantos
estão no piloto e depois, a janela e a próxima abertura de cada um, os andares escolhidos, o
piloto, a observação, o agendamento, o ligar pela rede, os reenvios, os excluídos (andar em
conflito) e os avisos.

Tudo é recalculado no servidor a partir dos IDs do formulário (o navegador não manda máquinas
nem contagens). A conferência leva uma impressão do plano (`escopo_hash`); na confirmação o
servidor recalcula e, se o escopo mudou (máquina entrou, saiu ou mudou de lugar, janela mudou),
mostra a conferência de novo em vez de criar.

## Etapas, piloto e observação

Ao confirmar, o servidor grava o escopo (`campaigns.scope`) e os alvos (`campaign_targets`: uma
linha por máquina, com a etapa, o prédio, o andar e como entrou). A chave da tabela é campanha +
máquina: a mesma máquina não pode estar em duas etapas.

- **Sem piloto**: uma etapa; todos os prédios em paralelo, cada máquina na janela do seu prédio.
- **Com piloto**: a etapa 1 libera só o piloto; quando todos os jobs dele terminam sem falha, a
  observação escolhida corre (os intervalos de sempre: 0 a 7 dias) e a etapa 2 libera o resto do
  escopo de uma vez, em paralelo, cada prédio na sua janela. O piloto não recebe de novo.

Os critérios de sempre continuam: qualquer job sem sucesso pausa a campanha (os limites de falha
não mudaram); retomar aceita as falhas da etapa atual; o reenvio pedido pelo admin e os
automáticos funcionam como antes.

**Piloto sem evidência**: se nenhum job do piloto termina com sucesso (ninguém precisava,
máquinas saíram do escopo, tudo dispensado), a campanha **pausa** com o motivo, em vez de
liberar o resto. Retomar é a decisão explícita de seguir mesmo assim.

## Agendamento

- **Com prazo**: como antes (o fechamento de programa aberto e o ligar pela rede não valem com
  prazo).
- **Na janela de cada prédio**: o job de cada máquina espera a próxima janela ativa do seu
  prédio. A criação é recusada se algum prédio do escopo não tem janela ativa (a mensagem lista
  quais) ou se há equipamentos sem localização no escopo. Se a janela de um prédio for removida
  ou desativada durante a campanha, a etapa que chega a ele **pausa** com o nome do prédio; nada
  sai fora da janela, nem pela janela de um grupo antigo.
- **Em horário fixo**: o horário da campanha (com a duração e o fuso do painel) vale para todos
  os prédios, no lugar da janela de cada um.

## Wake on LAN

A integração é a mesma (antecedência de 10 min, só as desligadas, agentes da mesma sub-rede,
registro dos pedidos, confirmação de quem ligou, desligar depois só as que o patchd ligou e sem
ninguém conectado). A referência é a janela do prédio de cada máquina ou o horário fixo. Só a
etapa liberada recebe pedidos: no piloto por andar, só as máquinas daquele andar; as das etapas
seguintes não são ligadas antes da liberação.

## Reenvios

O reenvio (manual ou automático) fica na etapa original e nos alvos dela: não libera ninguém
novo, respeita a janela do prédio, os limites de tentativas e os motivos de falha de sempre, e
os jobs continuam assinados como antes (o protocolo e o agente não mudaram). A máquina que saiu
do escopo recebe o reenvio como "dispensado" com o motivo. Um lançamento interrompido (o
servidor caiu no meio) não repete a máquina que já recebeu o job da etapa.

## Equipamentos movidos, aposentados ou com IP novo

O escopo não é reconsultado para decidir quem entra: equipamentos novos nos prédios não entram
depois da criação. Na liberação de cada etapa e em cada nova tentativa, o motor confere onde a
máquina está agora:

| Situação | O que acontece |
|---|---|
| Mudou de prédio | não recebe job: "mudou de prédio depois da criação da campanha" (nas liberações) |
| Mudou de andar, escolhida pelo andar ou no piloto por andar | não recebe job: "mudou de andar..." |
| Mudou de andar, escolhida pelo prédio inteiro | recebe normalmente (o prédio é o escopo) |
| Mudou de IP no mesmo andar | recebe normalmente |
| Aposentada ou excluída | não recebe job: "aposentada" |
| Sem localização, incluída explicitamente | recebe (o escopo autorizou) |

A elegibilidade técnica (pendências aprovadas, versão aprovada, instalador no cache, a busca do
Windows) continua conferida na hora de cada liberação, como antes.

## Acompanhamento

O detalhe da campanha mostra as etapas ("Piloto — Prédio, 1º andar → observação de 1 dia →
Demais equipamentos"), o piloto com quantos concluíram, o progresso geral, e a tabela **Progresso
por prédio** (e pelo andar piloto): concluídos, em execução, aguardando a janela, aguardando o
piloto, adiados, falhas, dispensados, sem job e a próxima janela. Os filtros, a paginação, o
reenvio e o CSV continuam; o CSV ganhou as colunas `etapa`, `predio` e `andar`.

As campanhas antigas aparecem como "legada (grupos de implantação)", com os grupos de sempre.

## Linha de comando

```sh
patchd-server building list
patchd-server building window -id 12,15 -days seg-sex -start 22:00 -end 04:00 [-tz America/Porto_Velho]
patchd-server building window -id 12 -disable | -enable | -clear
patchd-server campaign create -name "LibreOffice" -type thirdparty -app libreoffice -version 26.8.1.1 \
    -all-buildings -pilot-floor "12=1º andar" -observe 24h -window
patchd-server campaign create -name "Busca" -type rescan -buildings 12,15 -floor "20=Térreo" -ttl 6h
```

`-unlocated` inclui os sem localização (sem `-window`). `-rings` é recusado em campanhas novas.
`campaign show/list/pause/resume/cancel` funcionam com as duas espécies de campanha. `building
window` vai para a auditoria como `cli.building.window`.

## Shield

O Shield usa o mesmo resolvedor para os rankings de prédio, andar e sala. A refatoração não muda
o grupo de implantação das máquinas, então o escopo do Shield (os grupos piloto da política, se
houver), o ranking por grupo e as missões por grupo seguem como estavam, e nada é reiniciado:
pontuação, XP, medalhas, sequências, rankings, missões, exceções e histórico ficam preservados.
A adoção de escopo por prédio na política do Shield fica para uma etapa própria (hoje os
rankings físicos já cobrem a disputa entre prédios e andares).

## Segurança e auditoria

As ações são só do administrador, com a proteção de mesma origem do painel. Todo ID de prédio,
chave de andar e piloto é conferido no servidor contra a localização atual; o navegador não
amplia o escopo. Vão para a auditoria: a criação (com prédios, andares, piloto e etapas), as
janelas (individuais, em lote, legadas), pausas, retomadas, reenvios, cancelamentos e os
comandos da linha de comando.

## Banco de dados

A migração **0085_implantacao_predios** cria:

- `buildings` (ID, nome exato único, presença, primeira e última vez visto, nome anterior),
  preenchida com os prédios do patrimônio atual e, como ausentes, os que só aparecem em vínculos
  antigos de grupo ou de andar;
- `building_windows` (uma por prédio, com chave estrangeira e as mesmas restrições da janela dos
  grupos);
- `campaigns.scope` (jsonb, `NULL` nas campanhas antigas) e a restrição de que toda campanha nova
  tem grupos ou escopo;
- `campaign_targets` (chave campanha + máquina, índice por etapa, prédio com `ON DELETE SET
  NULL`, sem chave estrangeira para a máquina: a excluída continua no histórico).

Nada é reconstruído nem apagado: as tabelas dos grupos, as campanhas antigas, os jobs e o Shield
não mudam. A criação da campanha grava escopo e alvos numa transação; o motor segue sob a trava
consultiva das campanhas (uma instância por vez avança as campanhas) e é idempotente no
lançamento.

## Atualizar o servidor

1. `./deploy/atualizar.sh vX.Y.Z` (docs/operacao.md): o script faz a cópia do banco antes da
   troca, troca o servidor e diz que o banco mudou (migração 0085). A migração roda sozinha na
   partida (as instâncias esperam umas pelas outras pela trava das migrações) e não dispara
   nenhum job.
2. Se quiser, importe o patrimônio de novo (a migração já cadastrou os prédios atuais).
3. Em **Automação › Prédios e implantação**, configure as janelas: individualmente, em lote, ou
   aplicando as dos grupos antigos sem ambiguidade.
4. Confira os andares em **Configuração › Localizações**.

As campanhas em andamento continuam pelos grupos antigos até o fim. O agente não precisa ser
atualizado: o protocolo e a assinatura dos jobs não mudaram.

## Recuperação em caso de problema

- **Campanha nova com comportamento inesperado**: pause ou cancele pelo painel (os jobs
  pendentes são cancelados; os entregues seguem). As campanhas antigas não dependem das tabelas
  novas.
- **Janela errada**: corrija ou desative a janela do prédio; os jobs já criados mantêm a janela
  com que foram assinados (cancele a campanha para tirá-los da fila).
- **Voltar a versão anterior**: o servidor antigo recusa subir num banco com migração que não
  conhece. `./deploy/atualizar.sh --voltar --confirmo` restaura a cópia de antes da atualização;
  o que foi feito depois dela (janelas por prédio, campanhas por localização) se perde.
- **Renomeação reconhecida indevidamente**: a página mostra o nome anterior; reconfigure a
  janela do prédio se preciso (nenhum dado é apagado na troca).
