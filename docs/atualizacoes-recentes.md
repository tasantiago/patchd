# Atualizações recentes e compatibilidade

Revisão documental de **9 de novembro de 2026**, baseada nas mudanças da `main` entre [`82570079`](https://github.com/tasantiago/patchd/commit/82570079675abf30357957eedac5c8c5fea90e93) e [`e2c88860`](https://github.com/tasantiago/patchd/commit/e2c88860b187496b344917052d47055671a89d4c). Essas referências identificam o código, sem inventar um número de release para a instalação de cada organização.

## O que mudou

| Área | Mudança observada no código | Como disponibilizar / onde ler |
| --- | --- | --- |
| Início e Parque | Conformidade reaproveitada em memória, visão geral tolerante à reavaliação em massa, barras por grupo/sistema/job e gráficos de desempenho mais legíveis. | Atualizar servidor; [Dashboards](dashboard.md). |
| Vocabulário e navegação | Frota passou a Parque; Anéis passaram a Grupos de implantação. Textos longos podem ser expandidos por leia mais. O comando `ring` continua válido. | Atualizar servidor; [Manual](manual-do-usuario.md). |
| Saúde do servidor | CPU, memória, pool, banco, disco e tempo de resposta com recomendações de dimensionamento e histórico. | Servidor; [Saúde](saude-servidor.md). |
| Comunicação | Intervalo de check-in pela tela e canal em tempo real que avisa sobre novos jobs. | Servidor e agente compatível; [Canal](canal-tempo-real.md). |
| Consultas | Perguntas de leitura sobre arquivo, processo, serviço, Registro e programa, com agrupamento e CSV. | Servidor e agente compatível; [Perguntas](canal-tempo-real.md#fazer-uma-pergunta). |
| Máquinas fora da rede | Porta HTTPS só de agentes, indicação do caminho do último check-in e relé automático aprendido pelo agente. | Atualizar servidor/agente e preparar túnel/relé; [Relé](rele.md). |
| Grupos de implantação | Página do grupo, seleção e lista de computadores, regras por prédio do patrimônio e faixa de IP, com exceções manuais e conflitos. | Servidor; [Grupos](operacao.md#grupo-de-implantação-por-faixa-de-ip-ou-prédio). |
| Aprovações | Visão por correção, filtros e aprovação em lote do filtro completo, sem teto de 200 para essa modalidade; terceiros com aprovação da versão guardada mais nova. | Servidor; [Patch](operacao.md#aprovação-em-lote-e-filtros-em-patch). |
| Campanhas | Todas as atualizações do sistema e todos os terceiros aprovados, horário fixo, filtros/paginação de jobs e reenvios automáticos ou manuais por motivo. | Servidor; tipos e opções novos podem exigir agente compatível; [Campanhas](manual-do-usuario.md#acompanhar-reagendar-e-reenviar-campanhas). |
| Windows Update | Fonte reserva Microsoft Update, desligada por padrão, respeitando aprovações e bloqueios da GPO. | Servidor e agente compatível; [Fonte reserva](operacao.md#fonte-reserva-direto-da-microsoft). |
| Instaladores de terceiros | Prazo de 60/90 minutos, logs na falha, detecção de Windows Installer ocupado, detecção de programa fechado e opção de fechar com aviso em horário autorizado. | Agente atualizado; configuração de fechamento na campanha; [Instalações](operacao.md#prazo-da-instalação-e-a-fila-do-windows-installer). |
| Catálogo Windows | Regras de instalação de Acrobat Reader 64-bit e Python 3.14, com as restrições de tipo, arquitetura e escopo descritas no guia. | Atualizar servidor e preparar/aprovar os pacotes; [Catálogo](operacao.md#acrobat-reader-e-python-314). |
| Downloads no agente | Sem limite total fixo de dois minutos para os arquivos; até cinco tentativas de retomada após queda no corpo, com validação da mesma versão do arquivo. | Agente atualizado; [Downloads](operacao.md#downloads-grandes-no-agente). |
| Alertas e disco | Reconhecer/fechar em lote, fechamento manual identificado e limpeza de temporários a partir de alertas de disco, com conferência dos itens. | Servidor; limpeza exige agente compatível; [Alertas](monitoramento.md). |
| Wake-on-LAN | Pacote mágico enviado por agentes da mesma sub-rede e acompanhamento do primeiro check-in da máquina; opção de ligar antes de reinício marcado. | Servidor e repetidores compatíveis; [Ligar pela rede](wake-on-lan.md). |
| Logs e interface | Retenção dos logs de instalação, rotação de update/stderr, formulários alinhados e página de erro com cabeçalhos de segurança. | Conforme componente alterado; [Operação](operacao.md). |

## Aplicar as mudanças

1. Confira a versão e o commit da imagem em execução; faça o backup previsto no roteiro de atualização.
2. Atualize o servidor pelo [procedimento de versões](../README.md#gerar-e-publicar-uma-nova-versão-do-servidor-e-do-agente). As migrações são aplicadas no início do processo. Nesse intervalo foram acrescentadas as migrações **0054 a 0065**; não é necessário executá-las manualmente.
3. Para as funções do agente, gere e publique uma versão assinada compatível, preservando as chaves de jobs e releases. O servidor atualizado não troca sozinho um agente que não recebeu a release.
4. Configure as opções novas e confira um grupo piloto antes de ampliar a implantação. Prepare o relé e faça os agentes aprenderem o endereço enquanto ainda há contato direto.
5. Confira evidências e resultados, incluindo máquinas antigas, indisponíveis ou fora da rede. A presença do menu no servidor não comprova suporte de todos os agentes.

O padrão básico e os grupos de aplicativos/atalhos da [Padronização](padronizacao.md) já existiam na referência inicial desta revisão. Continuam distintos dos grupos de implantação das campanhas.

## Limites que continuam valendo

- macOS tem inventário e avaliação; instalação das atualizações do sistema pelo patchd continua fora do fluxo automatizado atual. Aplicativos por scripts são outra modalidade.
- Acordar máquinas não confirma que todas responderam. O ciclo completo de acordar na janela e desligar automaticamente depois continua como etapa futura do guia de Wake-on-LAN.
- O canal em tempo real e o relé mantêm a validação de TLS, credencial e jobs assinados. Não são acesso gráfico remoto nem terminal de comandos livres.
- Os reenvios de campanha não corrigem por si só assinatura, versão incompatível, GPO ou falta de espaço. Investigue o motivo antes de reenviar.
- Mudar somente porta ou fuso não exige recompilar o servidor: veja [Comunicação e horário](comunicacao-e-horario.md).
