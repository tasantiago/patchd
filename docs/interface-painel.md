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

## Homologação visual

Conferir com dados de homologação e com listas vazias: Início, Parque,
Inventário, Patch, Jobs, Campanhas, Shield (todas as abas), Configuração e
Localizações. Conferir desktop, janela estreita, zoom a 200%, teclado,
abas ativas, formulários longos, mensagens de erro e estados desabilitados.
Não confundir pontuação de segurança com posição de ranking. Nenhum dado
simulado deve ser inserido em produção.
