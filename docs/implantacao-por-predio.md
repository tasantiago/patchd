# Implantação por prédio — transição

O painel Automação › Grupos de implantação passa a apresentar primeiro os
prédios identificados pelo patrimônio. Exibe os equipamentos por prédio,
os sem localização, as regras/janelas do grupo vinculado e a distribuição
nos grupos técnicos existentes. O inventário pode ser aberto com o filtro
do prédio. Os grupos legados ficam em uma seção expansível.

## Limite desta etapa

Esta tela não altera máquinas, campanhas, jobs, regras ou janelas. Hoje
os grupos de implantação numéricos são parte do contrato de execução,
persistência, CLI, políticas e janelas (0 a 9). A localização física não
deve ser confundida com essa ordem técnica.

## Próxima etapa: substituir o escopo operacional

- Introduzir IDs estáveis por prédio (sem limite fixo de dez).
- Permitir campanhas com seleção explícita de prédios e ordem configurável;
  manter piloto como escopo opcional, independente da localização.
- Persistir janelas por prédio e congelar os alvos e janelas de cada campanha
  conforme a semântica de execução definida.
- Preservar campanhas em andamento e jobs assinados nos grupos originais;
  não reinterpretar automaticamente um grupo que contém vários prédios.
- Tratar equipamentos sem patrimônio em escopo explícito, nunca incluí-los
  silenciosamente em um prédio.
- Adaptar filtros, Shield, padronização, jobs, CLI, migrações e testes de
  integração antes de desativar os grupos legados.

A migração operacional exige homologação com campanhas ativas e inativas,
prédios compartilhando grupos, exceções manuais, falta de patrimônio e
janelas diferentes. Nenhum job deve ser disparado pela migração.
