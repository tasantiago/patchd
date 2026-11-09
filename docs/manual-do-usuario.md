# Manual de operação do painel

Este guia apresenta as funções disponíveis no código revisado em 9 de novembro de 2026. Os detalhes de configuração ficam nos guias vinculados em cada seção. Para instalação nova, comece pelo [README](../README.md#instalar-um-servidor-novo-do-zero).

## Começar pela visão geral

Depois do login, **Início** mostra conformidade, comunicação, segurança, jobs e campanhas. Use **Personalizar gráficos** para escolher os blocos desse navegador. Em **Parque**, investigue máquinas sem contato, pendências e fontes de busca com erro; o detalhe de cada computador mostra as evidências e suas datas.

O nome atual da lista é **Parque**. A documentação antiga pode chamá-la de **Frota**. O nome atual de **Anéis** é **Grupos de implantação**; o comando de terminal continua `patchd-server ring`, e as janelas e campanhas continuam associadas aos mesmos grupos numéricos.

Consulte [Dashboards](dashboard.md). Para lentidão do próprio servidor, use **Configuração › Saúde**, descrita em [Saúde do servidor](saude-servidor.md).

## Grupos de implantação e padronização

São configurações com finalidades diferentes:

| Configuração | Para que serve | Exemplo |
| --- | --- | --- |
| Grupo de implantação | Escolher a etapa de uma campanha e a janela de manutenção. Cada computador fica em um grupo de 0 a 9; o padrão é 1 e o piloto é 0. | Piloto primeiro; prédio administrativo depois. |
| Grupo de padronização | Definir aplicativos e atalhos desejados. Vários padrões podem se somar, por computador e/ou grupo do AD do usuário. | Padrão básico + Financeiro; padrão básico + RH. |

Na página de um **grupo de implantação**, confira as máquinas, adicione por seleção ou lista e configure regras por **prédio do patrimônio** ou **faixa de IPv4**. A regra de prédio prevalece sobre a de faixa; entre faixas, vence a menor. Empates que apontam grupos diferentes são sinalizados. Máquinas movidas à mão são exceções até serem devolvidas às regras. A passada automática por faixa ocorre a cada cinco minutos; remover uma regra não move a máquina de volta.

Em **Padronização**, crie o padrão básico e os padrões dos setores, selecione os destinos e ative após conferir. Windows usa o catálogo de instaladores aprovados; os três sistemas também podem usar scripts com detecção de presença pelo inventário. Atalhos são aplicados na sessão do usuário, preservando arquivos pessoais. Veja [Padronização](padronizacao.md) e [regras dos grupos de implantação](operacao.md#grupo-de-implantação-por-faixa-de-ip-ou-prédio).

## Aprovar e instalar atualizações

1. Em **Patch**, filtre por sistema, severidade, texto e situação. A visão **por correção** reúne os itens corrigidos pelo mesmo KB, versão do sistema ou pacote fonte.
2. Marque itens da página ou use **Conferir todas as N aguardando** do filtro. A aprovação acontece depois da conferência. Todos os resultados do filtro podem ultrapassar 200 itens; a seleção manual é limitada ao tamanho da página.
3. Em **Patch › Terceiros**, baixe os instaladores e aprove a versão guardada. Confira arquitetura e tipo MSI/EXE; programas sem regra de instalação continuam somente avaliados.
4. Crie a campanha, selecione os grupos em ordem e confira o plano antes de ativar.

As campanhas **Todas as atualizações do sistema** e **Todos os programas de terceiros aprovados** usam as aprovações existentes. A segunda atualiza programas já instalados; para instalar aplicativos ausentes, use padronização ou outro fluxo de instalação apropriado.

As atualizações do sistema são instaladas pelo patchd no Windows e Linux conforme o tipo e a fonte. macOS continua com inventário e avaliação; instalação automatizada das atualizações do sistema não está incluída. Não confunda isso com instalar aplicativos no macOS por scripts de padronização.

Para usar **Microsoft Update como fonte reserva**, habilite a função em **Configuração › Parâmetros › Agentes** e a opção correspondente na campanha. Ela vem desligada, exige agente compatível e respeita GPOs que bloqueiam esse acesso. A aprovação continua no patchd. Veja [aprovação em lote](operacao.md#aprovação-em-lote-e-filtros-em-patch) e [fonte reserva](operacao.md#fonte-reserva-direto-da-microsoft).

## Acompanhar, reagendar e reenviar campanhas

Use os contadores de estado, a busca de máquina, o filtro de programa e a paginação de jobs. Textos longos ficam disponíveis em **leia mais**.

| Ação | Efeito |
| --- | --- |
| Pausar | Interrompe a progressão conforme as regras da campanha. |
| Retomar | Aceita as falhas da etapa e permite a progressão; confira o motivo antes. |
| Reenviar | Reavalia as máquinas selecionadas da etapa atual e recria o que ainda precisa; não aceita automaticamente novas falhas. |
| Reenvios automáticos | De 0 a 5 por máquina para expirados e falhos; falhos aguardam uma hora. Recusados não são reenviados automaticamente. |

O botão **Reenviar** pode dispensar uma máquina que já ficou em dia. O padrão de reenvio automático é 0. Veja [reenvios](operacao.md#reenviar-o-que-não-deu-certo).

Para executar de madrugada, escolha **horário fixo** na campanha ou a **janela** do grupo. Um reinício individual também pode ser marcado por dia e hora; máquina desligada durante toda a validade deixa o job expirar. **Ligar pela rede 10 min antes** depende de um repetidor ligado na sub-rede e dos requisitos de Wake-on-LAN.

Programa aberto normalmente adia a instalação. Agentes compatíveis detectam quando ele fecha e antecipam o check-in. **Fechar o programa com aviso** é uma opção explícita para campanhas de terceiros com janela ou horário fixo; pode encerrar à força depois do aviso e perder trabalho não salvo. Veja [programa aberto](operacao.md#programa-aberto-na-instalação-de-terceiros).

## Alertas e limpeza de disco

Em **Alertas**, selecione linhas ou todos os abertos do filtro para **Reconhecer** ou **Fechar**, com conferência e nota. Reconhecer registra ciência; fechar manualmente reinicia a contagem, e um problema persistente pode abrir outro alerta.

Alertas de disco oferecem **Limpar temporários**, individualmente ou em lote. Confira as máquinas e os itens antes de confirmar. O job usa uma lista fechada de temporários e caches; não aceita caminhos livres nem apaga Documentos, Downloads ou Lixeira. As regras de idade e os itens aplicáveis variam por sistema. A limpeza de componentes/cache mais custosa vem desmarcada. Veja [Monitoramento e alertas](monitoramento.md).

## Agir em segundos e consultar o parque

O **Canal em tempo real**, ligado por padrão para agentes compatíveis, avisa sobre jobs novos pela mesma conexão de saída usada com o servidor. Sem canal, a entrega continua no check-in. O Resumo da máquina informa a conexão atual.

Em **Automação › Perguntas ao parque**, o admin consulta arquivo, processo, serviço, Registro ou programa, para todas as máquinas ou um grupo de implantação. As respostas são agrupadas e podem ser exportadas em CSV. A pergunta vale uma hora; ausência de resposta não significa que o item não existe. Veja [Canal e perguntas](canal-tempo-real.md).

## Notebooks fora da rede

O [relé na nuvem](rele.md) encaminha TCP por um túnel até a **porta só de agentes**. O TLS continua entre agente e servidor. O painel, a API de leitura e o enrollment não são atendidos nessa porta.

Configure **Relé para fora da rede** enquanto os agentes ainda alcançam o servidor, para que aprendam o endereço. Agentes atualizados alternam entre caminho direto e relé. A indicação **fora da rede** significa que o último check-in entrou pela porta marcada como relé; não é uma geolocalização.

## Manter a instalação

Use [Portas, comunicação e horário](comunicacao-e-horario.md) para alterar acesso sem recompilar e [Operação](operacao.md) para backup e recuperação. Para disponibilizar funções novas, atualize primeiro o servidor e publique uma versão assinada do agente quando necessário. Alterar configuração não exige gerar uma versão; obter código novo exige atualizar a imagem e/ou o agente.
