# Contribuindo com o patchd

Obrigado por querer contribuir. O patchd é um sistema com acesso privilegiado aos endpoints; mudanças pequenas, testadas e revisadas são preferidas.

## Comece aqui

1. Leia [README](README.md), [requisitos](docs/requisitos.md), [fontes de verdade](docs/fontes-de-verdade.md) e [segurança](docs/seguranca.md).
2. Se a Wiki estiver publicada, siga o guia de onboarding: https://github.com/tasantiago/patchd/wiki
3. Antes de programar, procure uma issue existente e confirme o escopo.
4. Execute apenas em laboratório e utilize fixtures sintéticas.

## Ambiente

As instruções oficiais estão no README. O fluxo inicial inclui `docker compose up -d --build`, `docker compose exec dev zsh` e `go build -o bin/ ./cmd/...`.

## Abrindo PR

- Associe issue e requisito, se existir.
- Descreva comportamento atual/esperado, estratégia de correção e limites.
- Inclua testes de regressão, negativos e validações por plataforma quando pertinente.
- Relate comandos executados, resultados e testes que não puderam ser feitos.
- Considere compatibilidade com versões antigas de agentes e migrations.
- Atualize a documentação técnica se alterar contrato ou segurança.
- Nunca inclua segredos, endereços internos, identificadores pessoais, certificados privados ou informações de equipamentos reais.

## Segurança

Mudanças em jobs, assinaturas, identidade, TLS, OIDC, software privilegiado, reboot e firmware devem ser revisadas por um mantenedor. **Não** implemente execução arbitrária de comandos remotos. Não rode testes de operações destrutivas em produção.

## Proposta de convenções

- Branches: `docs/assunto`, `test/assunto`, `fix/assunto`, `feat/assunto`.
- Commits: mensagens curtas e descritivas em português; assunto sem credenciais/dados reais.
- Issues: passos reproduzíveis e critérios de aceite.

As convenções são uma **proposta inicial** até validação pelos mantenedores.
