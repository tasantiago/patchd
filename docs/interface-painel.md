# Interface administrativa do PatchD

A apresentação do servidor usa templates Go e CSS local, sem JavaScript ou CDN.
A camada panelUXCSS preserva a navegação, ações e estados semânticos existentes.

- O tema compartilhado da interface administrativa melhora contraste, espaçamento,
  foco de teclado, legibilidade de tabelas e destaque da aba ativa.
- Início e Shield apresentam cabeçalhos de contexto, indicadores com hierarquia e
  estados explícitos. Rankings distinguem posições e mostram barras com números.
- Localizações mostra quantidade de faixas e máquinas por prédio, campos de
  edição associados à faixa e ação de gravação por prédio.
- Nenhuma ação nova de segurança é executada pela interface.

## Navegação por tarefa

O menu principal prioriza as tarefas de acompanhamento e operação. Os endereços
anteriores continuam funcionando, inclusive favoritos e links de relatórios.

| Entrada | O que encontrar |
| --- | --- |
| Computadores | Situação do parque, inventário de equipamentos e programas, e aposentadas. As três opções aparecem ao entrar nessa área. |
| Atualizações | Visão geral, sistema operacional e programas de terceiros nas abas da própria página. |
| Campanhas | Distribuição de tarefas, piloto, destinos e horários. |
| Execuções | A lista de tarefas enviadas aos agentes, antes chamada Jobs; o endereço continua `/painel/jobs`. |
| Pedidos de software | Solicitações feitas pela Central e decisões da TI, antes sob o nome Autoatendimento. |
| Ferramentas avançadas | Pacotes de scripts e perguntas ao parque, somente para administradores. O grupo abre ao acessar uma dessas ferramentas. |
| Configuração | Preferências, agentes, prédios e acesso; integrações, histórico, saúde e BIOS ficam em Mais configurações. |
| Ajuda e primeiros passos | Instruções por tarefa, com busca por termos comuns e técnicos. |

Auditoria e instalação em rede deixam de ocupar entradas próprias na barra
lateral e continuam acessíveis em Configuração. O histórico da Central fica em
Pedidos de software; a oferta de aplicativos fica em Configuração › Mais
configurações › Catálogo da Central.

## Ajuda dentro do servidor

`/painel/ajuda` exige uma sessão do painel e funciona sem consultar uma wiki ou
carregar recursos da internet. A busca ignora maiúsculas e acentos e reconhece
termos antigos, como jobs e patch. Os assuntos administrativos são oferecidos
somente ao perfil Administrador.

O link Ajuda no topo leva ao assunto da página atual. **Como usar esta página**
abre instruções curtas no próprio contexto. A ajuda explica diferenças como
aprovar versus instalar, reconhecer versus encerrar um alerta, e reenviar versus
retomar uma campanha. Ausência de contato ou de evidência não é apresentada como
confirmação de máquina desligada ou protegida.

A página Geral da Configuração apresenta cartões por tarefa. Diagnóstico e
versão do servidor ficam recolhidos. Em Preferências, o valor, a ajuda e os erros
do campo continuam visíveis; a origem técnica e a restauração ficam em **Origem
e restauração**. As ações de salvar, restaurar e confirmar mantêm suas regras.

As alterações são exclusivas do painel do servidor: não mudam a Central de
software, o agente, o protocolo, os dados ou as permissões. Para disponibilizá-las,
atualize a imagem do servidor; não é necessária uma nova versão do agente.

## Homologação visual

Conferir com dados de homologação e com listas vazias: Início, Parque,
Inventário, Patch, Jobs, Campanhas, Shield (todas as abas), Configuração e
Localizações. Conferir desktop, janela estreita, zoom a 200%, teclado,
abas ativas, formulários longos, mensagens de erro e estados desabilitados.
Não confundir pontuação de segurança com posição de ranking. Nenhum dado
simulado deve ser inserido em produção.
