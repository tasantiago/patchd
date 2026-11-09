# Documentação do patchd

Use os guias desta pasta junto com o código da versão instalada. A revisão de 9 de novembro de 2026 considerou a `main` até [`e2c88860`](https://github.com/tasantiago/patchd/commit/e2c88860b187496b344917052d47055671a89d4c). O que está no GitHub só chega à instalação depois de atualizar o servidor e, quando a função depende dele, o agente.

## Para operar

| Preciso… | Onde começar |
| --- | --- |
| Instalar um servidor novo com Docker ou Podman | [README: instalação do zero](../README.md#instalar-um-servidor-novo-do-zero) |
| Conhecer as funções atuais e o fluxo de trabalho | [Manual de operação do painel](manual-do-usuario.md) |
| Ver o que mudou recentemente e o que precisa de agente novo | [Atualizações recentes](atualizacoes-recentes.md) |
| Escolher os indicadores da página inicial | [Dashboards](dashboard.md) |
| Organizar computadores, aprovar e implantar atualizações | [Manual](manual-do-usuario.md#grupos-de-implantação-e-padronização) e [operação detalhada](operacao.md) |
| Definir aplicativos e atalhos por setor | [Padronização](padronizacao.md) |
| Executar um ZIP ou script avulso | [Pacotes de scripts](pacotes-de-scripts.md) |
| Enviar jobs em segundos e consultar máquinas | [Canal em tempo real e perguntas ao parque](canal-tempo-real.md) |
| Atender notebooks fora da rede | [Relé na nuvem](rele.md) |
| Alterar portas ou fuso sem compilar outra versão | [Portas, comunicação e horário](comunicacao-e-horario.md) |
| Investigar CPU, memória e disco dos computadores | [Monitoramento e alertas](monitoramento.md) |
| Dimensionar CPU, memória e banco do servidor | [Saúde do servidor](saude-servidor.md) |
| Ligar uma máquina pela rede | [Wake-on-LAN](wake-on-lan.md) |
| Atualizar servidor e publicar agentes | [README: versões](../README.md#gerar-e-publicar-uma-nova-versão-do-servidor-e-do-agente) e [build sem dev](build-agentes-sem-dev.md) |
| Fazer backup, restaurar ou diagnosticar a instalação | [Operação em produção](operacao.md) e [Podman](podman.md) |

## Para administrar e desenvolver

| Assunto | Guia |
| --- | --- |
| Requisitos, decisões e limites por plataforma | [Requisitos](requisitos.md) |
| Evidências de inventário e atualizações | [Fontes de verdade](fontes-de-verdade.md) |
| Autenticação, jobs assinados, chaves e ameaças | [Segurança](seguranca.md) |
| Guardas de acesso das rotas HTTP | [Matriz gerada de rotas](rotas.md) |
| Certificado e CA para laboratório | [TLS de laboratório](tls-laboratorio.md) |
| Senhas e configurações da BIOS | [Guarda e aplicação de senha](bios-senha.md) e [configurações](bios-config.md) |

A [Wiki](https://github.com/tasantiago/patchd/wiki) mantém as trilhas de formação e navegação. Para instruções que dependem da versão, prefira os documentos do mesmo checkout do servidor. O diretório `docs/` pode ser consultado mesmo sem acesso à Wiki.

## Manter os guias e a Wiki

Edite os guias neste repositório e revise os links junto com o código. O workflow [Documentação da wiki](../.github/workflows/wiki.yml) prepara as páginas operacionais quando esses documentos entram na `main`, preserva um bundle do histórico anterior e faz push sem força. As trilhas de formação são preservadas; a entrada, a navegação e os capítulos operacionais são atualizados a partir das fontes versionadas. O histórico anterior fica como artefato por 30 dias.

Para preparar os arquivos manualmente, clone `https://github.com/tasantiago/patchd.wiki.git` e execute, a partir deste repositório:

```bash
python3 scripts/exportar-wiki.py --destino /caminho/patchd.wiki
```

Confira o diff no clone da Wiki antes de commitar e publicar. O script não autentica, não faz push e não precisa de acesso aos agentes, ao banco ou aos segredos de produção. Consulte a execução do workflow para comprovar a publicação; uma alteração no repositório principal, sozinha, não comprova que a Wiki recebeu o conteúdo.

## Como interpretar o estado das máquinas

Um check-in com HTTP 200 confirma comunicação naquele instante. Um job concluído confirma o resultado aceito para aquele tipo de ação. Conformidade depende das evidências recentes de inventário e busca. Máquina sem resposta, agente antigo e operação não suportada não devem ser contados como sucesso.

Os perfis de leitura consultam as áreas permitidas do parque; alterações, perguntas ao parque, aprovações e operações administrativas exigem o perfil admin e ficam sujeitas às guardas e à auditoria de cada fluxo.
