# Página inicial e indicadores para decisão

Depois do login, `/painel/` abre a visão geral do ambiente. **Início** reúne os
indicadores; **Frota** (`/painel/frota`) mantém a lista completa, os filtros e a
exportação. Links antigos como `/painel/?estado=faltando` continuam funcionando.

A página usa as mesmas fontes do inventário, da conformidade e da operação.
Não cria uma pontuação de risco nem uma tendência histórica. O botão **Atualizar
visão** refaz a consulta e a página informa seu horário em UTC.

## Escolher os gráficos

Abra **Personalizar gráficos**, marque ou desmarque os blocos e clique em
**Salvar seleção**. Cada bloco pode ser habilitado separadamente: indicadores,
conformidade, segurança, sistemas, comunicação, execuções, campanhas,
acompanhamentos e equipamentos prioritários. **Restaurar todos** reativa a visão
completa. Também é possível ocultar todos e reconfigurar a página depois.

A seleção fica salva por até 180 dias para a conta e origem de login nesse
navegador. Ela não altera a página dos outros usuários nem exige migração do
banco. Limpar os cookies ou trocar de navegador volta à seleção padrão. As
preferências não alteram permissões. Consultas exclusivas de blocos desabilitados
não são executadas; os contadores normais do menu continuam seguindo seu cache.

## Como interpretar os cartões

| Indicador | Significado e decisão apoiada |
| --- | --- |
| Equipamentos ativos | Cadastros não aposentados. Abra o inventário para conferir a cobertura. |
| Sem contato recente | Último contato há mais de duas horas, ou nunca recebido. Investigue comunicação e disponibilidade; o indicador não prova que a máquina está desligada. |
| Alertas abertos | Alertas de desempenho ainda não encerrados, incluindo os já reconhecidos. A legenda informa quantos ainda aguardam reconhecimento. |
| Jobs sem sucesso em 24 h | Jobs que terminaram como falhos, recusados ou expirados nas últimas 24 horas, independentemente de quando foram criados. |
| Em dia | Equipamentos classificados como `em_dia` na avaliação atual. O percentual usa todos os equipamentos dessa avaliação, incluindo desconhecidos e sem dado recente. |
| Com exploração conhecida | Equipamentos com pelo menos uma correção pendente cuja exploração é conhecida ou inferida pelas fontes da avaliação. O valor conta equipamentos, não CVEs. |

**— indica dado indisponível**, nunca zero. Uma falha de consulta é sinalizada e
os demais blocos continuam sendo apresentados quando suas fontes respondem.
Uma frota vazia não recebe taxa de conformidade de 100%.

## Gráficos e acompanhamentos

- **Conformidade:** distribuição nos cinco estados exclusivos da avaliação.
  Equipamento sem contato por mais de sete dias é classificado como sem dado
  recente; esse limite tem outra finalidade que o contato recente de duas horas.
- **Composição:** famílias dos sistemas informadas no inventário dos equipamentos
  ativos. Até seis famílias são destacadas; as demais ficam em Outras famílias.
- **Segurança:** equipamentos com correções pendentes, exploração conhecida,
  estado principal de reinício pendente e avaliação desconhecida. As categorias
  podem se sobrepor; seus percentuais não formam uma divisão de 100%.
- **Comunicação:** divisão entre contato recebido nas últimas duas horas e
  ausência de contato recente, incluindo quem nunca fez contato.
- **Campanhas:** distribuição do estado das até 200 campanhas mais recentes.
- **Execuções:** estado atual dos jobs **criados** nos últimos sete dias.
  A contagem de falhas em 24 horas usa a data de término e, por isso, pode incluir
  jobs que não aparecem nesse grupo de sete dias. Conclusão de job não comprova
  conformidade: é necessário receber a evidência posterior do equipamento.
- **Decisões e acompanhamentos:** alertas não reconhecidos, execuções sem sucesso,
  falhas de fontes de busca, pedidos aguardando a TI e campanhas pausadas.
  Pedidos são consultados até 500; quando o limite é atingido, a legenda informa
  que há pelo menos esse número. Campanhas consideram as 200 mais recentes.

Cada indicador leva à tela correspondente para investigar a causa e decidir a
ação. Nenhum job é emitido, campanha retomada ou pedido aprovado pela página
inicial.

## Equipamentos para priorizar

A tabela mostra até oito equipamentos: primeiro os que têm exploração conhecida,
depois a ordem dos estados de conformidade, seguida do contato mais antigo. Em
empates, a ordem é estável pelo identificador. Ela mostra o motivo, as pendências
com sua unidade e o último contato; o link abre as evidências do equipamento.

Avisos de catálogo atrasado continuam visíveis. Ausência de dados, busca com falha
e estado desconhecido não são considerados evidência de conformidade. Uma
avaliação e a listagem de inventário são consultas distintas: mudanças durante a
consulta podem aparecer no próximo carregamento.

## Acesso e operação

Início e Frota exigem uma sessão válida e podem ser lidos pelos perfis de leitura
e administrador. Ações administrativas continuam nas telas e guardas existentes.
Nomes e motivos são escapados pelo template. Gráficos usam HTML/CSS do próprio
painel, com valores em texto, sem JavaScript, CDN ou relaxamento da CSP.

Não há nova configuração, migration ou chave para habilitar a página. Sem o
serviço de conformidade com PostgreSQL e catálogo, os blocos dessa avaliação
ficam indisponíveis; os demais indicadores usam suas próprias consultas.
