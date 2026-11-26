# Atende+ Shield — Missão 100%

> *Sua máquina protegida. Nosso ambiente mais seguro.* (o slogan, o nome do módulo e o da missão
> são configuráveis em Configuração › Shield; nada de nome de instituição no código.)

O Shield transforma o estado de proteção de cada computador numa pontuação de 0 a 100%, mostra
para a pessoa o que falta na Central de software ("Minha Proteção") e recompensa quem mantém a
máquina em dia: sequência de dias protegidos, XP, níveis, conquistas e missões. Para a TI, dá o
retrato do parque, de quem é cada pendência (usuário, TI, automação, fator externo) e o ranking
coletivo por grupo de implantação.

**O Shield não muda o compliance.** O estado de compliance (em dia, faltando, reboot pendente…)
continua sendo o da auditoria e dos relatórios. O Shield lê as mesmas evidências e calcula outra
coisa: o quanto da proteção está comprovada.

## 1. Diagnóstico (o que já existia e foi reaproveitado)

| Necessidade do Shield | De onde vem (sem coleta nova) |
|---|---|
| Atualizações do SO pendentes, críticas/exploradas | avaliação de compliance em cache (`complianceService`, `ComplianceStatus.Pending/Exploited`, `PendingItem.Severity`) |
| Programas de terceiros desatualizados (os fora de suporte não contam) | `MachineItems.ThirdParty` (manifesto, Aula 13.3) |
| Reinício pendente | última busca (`MachineSummary.RebootPending`) |
| Padronização | `standardization_states` (plano por conta e resultado dos atalhos) |
| Inventário atualizado | `ComplianceStatus.InventoryCollectedAt` |
| Agente atualizado | versão do check-in × versão publicada (`releases.Current`) |
| Quem usa a máquina | sessões do check-in (`machines.state->'users'`) e a identidade do token do Windows no canal local |
| O que a pessoa pode resolver sozinha | ofertas de "atualizar" sem aprovação do autoatendimento |
| Trava entre servidores | `store.WithAdvisoryLock` (nova chave `LockShield`) |
| Parâmetros, auditoria, CSRF, perfis | os do painel (sessão, `sameOrigin`, `adminAction`, `a.audit`) |
| Métricas | `internal/metrics` (sem rótulo de máquina ou pessoa) |

**Lacuna conhecida — antivírus:** o agente não coleta o estado do antivírus. O critério existe,
com o peso da especificação (5), mas **nasce desligado**: um critério sem evidência é
"desconhecido", não soma e impediria o selo de todo o parque. Ligar só depois de existir a coleta.

## 2. Componentes novos

| Onde | O quê |
|---|---|
| `internal/shield` | motor puro (sem banco e sem rede): política e validação, critérios, pontuação, selo, responsabilidade, sequência, XP, níveis, conquistas |
| `internal/store/shield.go` + migração `0082_shield.sql` | política versionada, pontuações, retrato diário, pessoas, presença, XP, conquistas, missões, exceções |
| `internal/api/shield.go` | avaliador periódico (5 min) sob trava consultiva; fechamento do dia |
| `internal/api/shieldagent.go` | rota do agente `POST /api/v1/agent/shield` (a "Minha Proteção" da pessoa) |
| `internal/api/shieldpanel.go` | Configuração › Shield (visão geral, equipamentos, pessoas, missões, exceções, ranking, política, auditoria, CSV) |
| `internal/localapi` | operação `shield` do canal local (identidade pelo token) |
| `internal/userapp` | aba "Minha Proteção" na Central — a página inicial dela quando o Shield está ligado (sem ele, Atualizações) — com anel SVG, critérios, pendências, sequência, nível, conquistas, missões, e o estado no ícone da bandeja |

Nada de Redis, fila ou serviço externo: o PostgreSQL já é a fonte de verdade, e a trava consultiva
resolve a concorrência entre servidores.

## 3. A pontuação

`pontuação = 100 × Σ obtidos / Σ aplicáveis`, sobre os critérios ligados:

| Critério | Peso padrão | Obrigatório p/ selo | Prazo padrão |
|---|---|---|---|
| Atualizações do sistema | 30 | sim | 7 dias (críticas/exploradas: 2) |
| Programas de terceiros | 25 | sim | 7 dias |
| Sem reinício pendente | 15 | sim | 1 dia |
| Padronização | 15 | não | 2 dias |
| Inventário em dia | 5 | não | parcial > 3 dias, não conforme > 7 |
| Agente atualizado | 5 | não | 7 dias |
| Antivírus ativo | 5 | não | (desligado: sem coleta) |

Cada critério tem uma situação, e a situação vale uma fração do peso (configurável, com limites):

| Situação | Vale | Conta no denominador |
|---|---|---|
| conforme | 1 (fixo) | sim |
| dentro do prazo | 0,75 | sim |
| parcial | 0,5 | sim |
| aguardando a TI | 0,5 | sim |
| aguardando o usuário | 0,25 | sim |
| não conforme | 0 (fixo) | sim |
| desconhecido | 0 (fixo) | **sim** — o que não se sabe nunca soma |
| não se aplica | — | não |
| exceção aprovada | — | não (mas, em critério obrigatório, **impede o selo**) |

Regras que a política não consegue relaxar (validadas no servidor):

- conforme vale 1; não conforme e desconhecido valem 0; as demais valem menos que 1;
- o número mostrado é arredondado **para baixo** e **nunca é 100 sem o selo** (99,99 aparece 99);
- o **selo 100%** exige: todos os critérios aplicáveis conformes, nenhum obrigatório em exceção ou
  desconhecido, nenhuma correção crítica/explorada pendente, nenhum reinício pendente e contato
  recente com o servidor;
- máquina sem contato há mais de 7 dias: todos os critérios ficam desconhecidos (responsabilidade
  externa) e a faixa é "sem dados suficientes".

O prazo conta de quando o critério saiu de conforme (guardado na avaliação anterior). Na primeira
avaliação depois de ligar o Shield, todo critério fora começa o prazo nesse momento.

**Responsabilidade** (de quem é a ação que falta): reinício e programas que a Central oferece
para a própria pessoa atualizar → usuário; correções do SO e falhas da padronização → TI; fila da padronização, agente e inventário → automação; sem contato → externa.
A responsabilidade principal da máquina é a do maior peso perdido.

## 4. Sequência, XP, níveis e conquistas

**Pessoa** = a conta (DOMINIO\usuario, em minúsculas). Quando a Central fala com o agente, o SID
do token entra junto, e passa a mandar: a conta renomeada continua a mesma pessoa.

**Presença:** quem tem sessão ativa (console ou área de trabalho remota) numa máquina do Shield
no check-in, ou abre a "Minha Proteção" nela, "usou a máquina no dia". Máquina compartilhada:
o dia dela conta para todos que a usaram. Quem usou duas máquinas só tem o dia protegido com as
duas no selo.

**Fechamento do dia** (no fuso da política; vazio: o do painel), um dia depois do outro:

| Dia | Efeito na sequência |
|---|---|
| fim de semana / feriado / não usou máquina | ignorado |
| todas as máquinas da pessoa no selo | **soma** (e dias protegidos +1) |
| faltou algo que não é da pessoa (TI, automação, sem dado, dentro do prazo) | **pausa**: não soma, não quebra |
| pendência vencida que é da pessoa (reinício adiado além do prazo, atualização oferecida na Central e não feita) | **quebra**: volta a zero |

**XP** (só eventos do servidor; não existe rota que receba XP do cliente):

| Evento | Padrão | Chave única |
|---|---|---|
| dia útil protegido | 10 | pessoa + dia |
| bônus de sequência (5, 10, 22, 66 dias) | 20/40/80/200 | pessoa + marco + dia |
| reiniciou quando havia reinício pendente | 15 | pessoa + máquina + dia |
| programa atualizado/instalado pela Central (job concluído) | 5 | pessoa + job |
| missão cumprida | o da missão | pessoa + missão |
| conquista | o da conquista | pessoa + código |
| ajuste do administrador | ± até 10.000 | o formulário (reenvio não soma), com motivo, autor e auditoria |

Teto diário (padrão 60) sobre tudo menos missões e ajustes. `UNIQUE (player_id, kind, ref)` no
banco: o mesmo evento gravado por dois servidores, ou o mesmo dia fechado duas vezes, conta uma vez.

**Níveis** (pelos dias protegidos acumulados, configuráveis): Bronze — Protetor (1), Prata —
Sentinela (7), Ouro — Guardião (15), Platina — Escudo de Elite (30), Diamante — Guardião Digital
(90), Mestre (180), Lendário (365).

**Conquistas** (declarativas: medida ≥ mínimo, configuráveis): primeiro escudo, semana blindada,
quinzena segura, mês de ferro, trimestre imbatível, reinício na hora, reinício exemplar, missão
cumprida, veterano de missões, volta por cima, centurião, atualizador, mil de XP.

## 5. Missões

Estados gravados: rascunho → publicada → (cancelada). Agendada, ativa, concluída e encerrada saem
das datas e do progresso. **Individual:** N dias protegidos dentro do período. **Coletiva:** X% das
máquinas do escopo (um grupo de implantação ou o parque do Shield) no selo, ou com um critério
conforme; o denominador inclui as sem dado. Cumprida, o XP vai para quem usou máquinas do escopo no
período. A missão não cria job, campanha nem mecanismo paralelo de implantação: só mede.

## 6. Ranking e privacidade

- **Coletivo** (padrão): por grupo de implantação — máquinas, % no selo, média. Grupos com menos
  máquinas que o mínimo (padrão 5) não aparecem.
- **Individual** (desligado por padrão): nome de exibição, nível, XP e sequência. Nunca máquina,
  vulnerabilidade ou pendência.
- Em nenhum ranking aparece CVE, programa vulnerável ou nome de máquina.
- **Por lugar** (ligados por padrão; cada um pode ser desligado na Política):
  - **prédio**: o `DEPREDIO` do patrimônio (Configuração › Patrimônio), ligado pelo tombo no
    nome da máquina; mínimo de máquinas como nos grupos;
  - **andar**: identificado pela faixa mais específica das regras de IP dos grupos de implantação.
    Em [Configuração › Localizações](localizacoes.md), o administrador nomeia as faixas por prédio
    e pode usar o mesmo nome para agregar várias faixas num único andar físico. O cadastro
    independe do ranking e do Shield. Os nomes antigos da política ainda valem na ausência
    de um vínculo físico. O ranking aparece quando há mais de um andar identificado no prédio;
    sem nome, a Central mostra "Faixa N" — **a faixa de IP nunca chega à Central**;
  - **sala**: o `DESALA` do patrimônio, dentro do prédio; mínimo próprio (padrão 3), porque
    sala de 1 ou 2 computadores identifica a pessoa.
  - A Central mostra os 10 primeiros prédios e o da máquina, e, no prédio dela, os andares e as
    10 primeiras salas e a dela, com "está em Nº de M". Máquinas sem prédio no patrimônio ficam
    fora desses rankings; sem dado contam como zero, como nos grupos.

## 7. Banco (migração 0082)

`shield_policies` (versões), `shield_scores` (atual por máquina), `shield_daily` (retrato do dia,
400 dias), `shield_players`, `shield_presence` (400 dias), `shield_xp` (livro do XP),
`shield_badges`, `shield_missions`, `shield_exceptions`. A migração `0083` grava o lugar
(prédio, sala, faixa) junto da pontuação e o ranking pronto por lugar (`shield_rankings`), que o
avaliador troca inteiro quando muda (qualquer servidor mostra o mesmo). Chaves estrangeiras com `ON DELETE
CASCADE` nas máquinas; chaves únicas para a idempotência; nenhuma migração publicada foi alterada.

## 8. API

| Rota | Quem | O quê |
|---|---|---|
| `POST /api/v1/agent/shield` | agente (credencial da máquina) | a "Minha Proteção": a máquina pela credencial, a pessoa pelo token lido pelo agente |
| `GET /painel/shield…` | painel (leitura) | visão geral, equipamentos, pessoas, missões, exceções, ranking, CSV |
| `POST /painel/shield/…` | admin, com CSRF (mesma origem) | política, missões, exceções, ajuste de XP, zerar a pontuação, grupos de competição — tudo auditado |

A máquina nunca vem de parâmetro (IDOR): vem da credencial. A pessoa nunca vem do app: vem do
token do Windows, como no autoatendimento.

## 9. Agente e Central (compatibilidade)

O agente novo anuncia `shield-v1` no check-in. A Central só mostra "Minha Proteção" quando o
agente responde à operação `shield`; agente antigo responde "operação desconhecida", e a aba
simplesmente não aparece. O servidor antigo não tem a rota: o agente novo recebe 404 e a Central
também não mostra a aba. Nada de reinício silencioso: o botão "Reiniciar" leva à aba Reinício,
com as mesmas regras de hoje.

## 10. Desempenho (5–10 mil máquinas)

- A avaliação de compliance já está em cache; o Shield lê o parque inteiro em ~8 consultas por
  passada (sem N+1) e só grava as pontuações que mudaram (ou a cada hora), em lotes de 500.
- O retrato do dia só é regravado quando muda; a presença é um `INSERT … SELECT unnest`.
- Uma passada por vez no cluster (`pg_try_advisory_lock`); quem não pega a trava pula a vez.
- A "Minha Proteção" lê uma linha de pontuação e uma pessoa (índices pela chave).
- O fechamento do dia grava uma transação curta por pessoa, com até 6 em paralelo.

Medido neste repositório (`TestShieldDezMil`, com `PATCHD_SHIELD_PERF=1`, PostgreSQL 16 local,
10.000 máquinas e 10.000 pessoas): primeira passada (grava tudo) ~1,5 s; passada sem mudança
~0,7 s; passada com o fechamento do dia ~7 s. É uma medida de laboratório, não de produção.

## 11. Riscos e mitigações

| Risco | Mitigação |
|---|---|
| Pontuação "bonita" sem prova | desconhecido conta e vale 0; selo com regras fixas; nunca mostra 100 sem selo |
| Culpar a pessoa pelo que é da TI | responsabilidade por critério; quebra só com pendência vencida que é dela |
| Manipulação de XP | só eventos do servidor; chaves únicas; teto diário; sem rota de XP; ajuste só de admin, auditado |
| Exposição em ranking | coletivo por padrão; grupo mínimo; individual desligado e sem dados técnicos |
| Carga no banco | lote, só o que mudou, trava, cache |
| Política inválida | validada ao gravar; inválida no banco → módulo desligado e erro no log |
| Agentes antigos | capacidade anunciada; a Central esconde a aba |

## 12. Critérios de aceite

1. Com o Shield desligado (padrão), nada muda para ninguém.
2. Ligado para um grupo piloto, só as máquinas dele recebem pontuação.
3. Máquina com tudo comprovado tem selo 100%; faltando qualquer evidência obrigatória, não tem.
4. Nenhuma tela mostra 100% sem o selo.
5. O mesmo dia fechado duas vezes (ou por dois servidores) não muda XP nem sequência.
6. Dia de pendência da TI não quebra a sequência; reinício adiado além do prazo quebra.
7. Ajuste de XP exige motivo, fica no livro do XP e na auditoria.
8. Ranking coletivo não mostra grupo menor que o mínimo; individual vem desligado.
9. CSV sem injeção de fórmula.
10. Rotas do painel com perfil e CSRF; a do agente só com a credencial da máquina.

## 13. Testes

- `internal/shield`: fórmula, arredondamento, desconhecido, sem contato, crítica, prazos, exceção,
  não aplicável, sequência (pausa/quebra/idempotência/volta por cima), XP e teto, conquistas,
  níveis, validação da política e fuzz da pontuação.
- `internal/store` (PostgreSQL): política versionada, lotes, pessoa por SID (renomeada),
  idempotência e teto do XP, ajuste, presença compartilhada, retenção, exceções e missões.
- `internal/api` (PostgreSQL): avaliador de ponta a ponta (piloto, selo, presença, fechamento
  idempotente entre servidores, XP do reinício, chave geral), rotas do painel e do agente.

## 14. Operação

1. Configuração › Shield › Política: escolha os grupos piloto, revise pesos e prazos, ligue.
2. Acompanhe a visão geral por alguns dias (a evolução diária mostra o efeito).
3. Ajuste o que gerar injustiça (prazos, créditos), crie missões, e só então amplie o piloto.

## 15. Estado da implementação e limitações conhecidas

Implementado: motor, política versionada, avaliador com trava, retrato diário, presença,
sequência, XP com teto e chaves únicas, níveis, 13 conquistas, missões individuais e coletivas,
exceções, ranking coletivo (painel e Central) e individual (painel, desligado), Minha Proteção
na Central, texto e avisos do ícone, painel com 8 abas, CSV, auditoria e métricas.

Limitações (honestas):

- **Antivírus:** sem coleta no agente; o critério nasce desligado.
- **Prazo:** conta da primeira avaliação em que o critério apareceu fora; ao ligar o Shield, tudo
  que já estava fora começa o prazo naquele momento.
- **"A Central oferece para você atualizar":** é aproximado pelas ofertas de atualização sem
  aprovação da TI; não confere os grupos do AD da pessoa (o servidor não tem o token dela no
  avaliador).
- **XP de atualização:** vale para qualquer job do autoatendimento de programa concluído (inclui
  instalação nova), com o teto diário.
- **Feriados:** lista manual na política (sem calendário externo).
- **Ícone e avisos no Windows:** o código da bandeja compila para Windows e a lógica dos avisos
  tem teste, mas a exibição real no Windows não foi executada neste ambiente (Linux).
- **Sem PostgreSQL** (servidor em memória), o Shield não existe: a aba avisa e a Central não o
  mostra.

### Programas de terceiros no detalhe

O critério "Programas atualizados" lista os desatualizados pelo nome, com a versão instalada e a
nova ("Google Chrome 158.0 → 159.0"; até 6, depois "e mais N"). Os **fora de suporte não entram
na pontuação** nem no detalhe: a troca deles é decisão da TI (e muitos são dependências que a
pessoa não controla, como os Visual C++ antigos); o painel continua mostrando-os em Patch ›
Terceiros. Na Central, o programa em dia com a versão aprovada pela TI, mas com versão mais nova
do fabricante ainda não aprovada, aparece em "Versão nova a caminho" (agente novo), não em "Em dia".

A referência do Google Chrome é a versão que o Google serve a ao menos metade do canal estável
(ou a marcada como fixável); as da liberação antecipada (0,5%) e as listadas sem distribuição não
contam, porque o instalador público ainda entrega a anterior.

### O que a Central mostra dos rankings

A Central mostra, nesta ordem:

1. **O grupo de competição** do prédio da máquina, se ele estiver em um (veja abaixo): os prédios
   do grupo, com a posição dentro do grupo ("Seu grupo — nome"), ou os andares do prédio, no
   grupo por andar ("Andares do prédio").
2. **Todos os prédios**: o ranking geral (os 10 primeiros e o da máquina), sem os prédios dos
   grupos com ranking geral à parte. Quem está num grupo à parte vê só o ranking do grupo.

Fora do grupo por andar, a Central **não mostra andares**: num prédio pequeno, o andar tem poucas
pessoas e o nome dele as identifica (LGPD). O ranking por **grupo de implantação** e o por
**sala** ficam só no painel do admin: os grupos numéricos são da implantação legada (quase todo o
parque está no grupo 1) e não dizem nada à pessoa, e o nome de uma sala pequena (um gabinete, por
exemplo) identifica quem trabalha nela. Os rankings de andar e sala continuam calculados e podem
ser ligados ou desligados na política. Um ranking com menos de 2 participantes (prédios ou
andares com o mínimo de máquinas da política) não aparece: disputa de um só não é disputa.

### Grupos de competição

Em **Shield › Ranking › Grupos de competição** (o admin cria, altera e exclui; tudo auditado,
`shield.competicao` e `shield.competicao_excluir`):

- **Grupo de prédios:** um nome e de 2 a 200 prédios, que disputam entre si (prédios parecidos,
  como os fóruns do interior). A ordem é a do ranking geral; a posição, dentro do grupo.
- **Grupo por andar:** um prédio com **mais de 3 andares** (contados pelas máquinas do parque em
  cada andar de Configuração › Localizações), em que os andares disputam entre si. Sem nome, fica
  o do prédio. Se o prédio depois ficar com 3 andares ou menos, o painel avisa.

- **Ranking geral à parte** (opção do grupo de prédios): os prédios do grupo **saem do ranking de
  todos os prédios** (as posições dos outros são refeitas sem eles) e a Central deles mostra só o
  ranking do grupo, com o nome do grupo como título (ex.: "Fóruns digitais"). É para prédios
  pequenos das cidades, que não disputam em pé de igualdade com as sedes: viram um "ranking geral
  dos fóruns digitais" ao lado do ranking geral. A aba Ranking lista quem ficou de fora do geral
  (migração 0088).

Cada prédio fica em um grupo só (o banco recusa e diz em qual grupo ele já está). O grupo é ligado
ao ID estável do prédio (migração 0085): a renomeação do patrimônio não tira o prédio do grupo.
O prédio sem grupo vê só o ranking geral. A aba mostra a disputa de cada grupo como a Central a
monta e avisa quando a Central ainda não o mostra (ranking desligado na política, ou menos de 2
participantes com o mínimo de máquinas). Tudo é do servidor: vale sem atualizar o agente
(migrações 0087 e 0088).

### Zerar a pontuação

Para recomeçar a contagem (o início do programa, o fim de um período de testes): **Shield ›
Pessoas › Zerar a pontuação**, só para o administrador, com motivo e a palavra `ZERAR` digitada
para confirmar. Numa transação (`ResetShieldPoints`, migração 0086), para **todas as pessoas**:

- o XP volta a 0, a sequência (atual e melhor), os dias protegidos (o nível), as voltas por cima
  e as conquistas são apagados, e o livro do XP é esvaziado;
- o dia de hoje (no fuso da política) conta inteiro no fechamento de amanhã; o último dia que
  fica para trás é ontem;
- nada de antes volta: a sequência de cada pessoa passa a ter ontem como último dia fechado, e o
  `CommitShield` não grava XP, sequência nem conquista de um dia até o do último zerar (o servidor
  que refaz os últimos 7 dias na partida, ou outro servidor no meio do zerar, não devolve
  pontos);
- a missão coletiva cumprida antes de zerar não paga de novo; a individual conta só os dias
  protegidos depois de zerar.

Não mudam: a pontuação de proteção das máquinas, os retratos do dia, os rankings de prédio e
andar (são das máquinas), as pessoas (conta, nome, presenças), as exceções e as missões. Não há
desfazer; cada zerar fica em `shield_resets` (quem, quando, motivo, quantas pessoas, o XP somado
antes), na auditoria (`shield.zerar`) e no aviso da aba Pessoas. Zerar de novo no mesmo dia
pode; para um dia antes do último zerar, não.
