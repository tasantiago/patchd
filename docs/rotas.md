# Matriz de rotas

Gerada pelo teste `TestMatrizDeRotas` (internal/api/rotas_test.go) a partir do `api.go`: não edite à mão.
Para atualizar: `go test ./internal/api -run TestMatrizDeRotas -update`. O teste `TestMatrizDeRotasNaPratica`
chama cada rota sem sessão, com a credencial de uma máquina, com sessão de leitura e, nos formulários,
de outra origem. As rotas do processo (`/healthz`, `/repo/`, o proxy do apt e a porta de métricas)
ficam em cmd/server e estão no modelo de ameaças (docs/seguranca.md).

## Pública (7)

Quem pode: qualquer um. Sem sessão: atende.

| Método | Caminho | Por que é aberta |
|---|---|---|
| POST | `/api/v1/enroll` | registro da máquina: exige um token de enrollment no corpo |
| GET | `/painel/entrar` | tela de login |
| GET | `/painel/estilo.css` | folha de estilo do painel (Aula 20.4): a tela de login também usa |
| GET | `/painel/favicon.svg` | ícone |
| GET | `/painel/logo` | logotipo da tela de login (Aula 19.1) |
| GET | `/painel/oidc/entrar` | início do login pelo RHBK (state, nonce e PKCE) |
| GET | `/painel/oidc/retorno` | volta do RHBK (confere state, nonce e o ID token) |

## Formulário público (2)

Quem pode: qualquer um, só da própria origem. Sem sessão: atende. De outra origem: 403.

| Método | Caminho | Por que é aberta |
|---|---|---|
| POST | `/painel/entrar` | login (conferência de origem, limite de tentativas) |
| POST | `/painel/sair` | saída (conferência de origem) |

## Agente (14)

Quem pode: máquina registrada (credencial). Sem sessão: 401.

| Método | Caminho |
|---|---|
| POST | `/api/v1/agent/canal` |
| POST | `/api/v1/agent/checkin` |
| GET | `/api/v1/agent/content/bundles/{file}` |
| GET | `/api/v1/agent/content/pacotes/{file}` |
| GET | `/api/v1/agent/content/wsusscn2/{sha256}` |
| POST | `/api/v1/agent/inventory` |
| POST | `/api/v1/agent/jobs/{id}/result` |
| GET | `/api/v1/agent/releases/{version}/{file}` |
| POST | `/api/v1/agent/scan` |
| POST | `/api/v1/agent/selfservice/catalog` |
| GET | `/api/v1/agent/selfservice/logo` |
| POST | `/api/v1/agent/selfservice/request` |
| POST | `/api/v1/agent/standardization/report` |
| POST | `/api/v1/agent/standardization` |

## Sessão (página) (21)

Quem pode: sessão do painel (leitura ou admin). Sem sessão: 303 → /painel/entrar.

| Método | Caminho |
|---|---|
| GET | `/painel/alertas` |
| GET | `/painel/aneis` |
| GET | `/painel/aposentadas` |
| GET | `/painel/autoatendimento` |
| GET | `/painel/campanhas/{id}` |
| GET | `/painel/campanhas` |
| GET | `/painel/frota.csv` |
| GET | `/painel/frota` |
| GET | `/painel/grupos-implantacao/{ring}` |
| GET | `/painel/grupos-implantacao` |
| GET | `/painel/inventario.csv` |
| GET | `/painel/inventario` |
| GET | `/painel/jobs` |
| GET | `/painel/maquinas/{id}/pendencias.csv` |
| GET | `/painel/maquinas/{id}` |
| GET | `/painel/parque.csv` |
| GET | `/painel/parque` |
| GET | `/painel/patch/item` |
| GET | `/painel/patch/programa` |
| GET | `/painel/patch` |
| GET | `/painel/{$}` |

## Sessão (API) (6)

Quem pode: sessão do painel (leitura ou admin). Sem sessão: 401.

| Método | Caminho |
|---|---|
| GET | `/api/v1/compliance` |
| GET | `/api/v1/identity-links` |
| GET | `/api/v1/machines/{id}/compliance` |
| GET | `/api/v1/machines/{id}/inventory` |
| GET | `/api/v1/machines/{id}/scan` |
| GET | `/api/v1/machines` |

## Admin (leitura) (14)

Quem pode: sessão de admin. Sem sessão: 303 → /painel/entrar. Sessão de leitura: 403, com o evento acesso.negado na auditoria.

| Método | Caminho |
|---|---|
| GET | `/painel/configuracao/agente/arquivo/{name}` |
| GET | `/painel/configuracao/agente/ca.crt` |
| GET | `/painel/configuracao/auditoria.csv` |
| GET | `/painel/configuracao` |
| GET | `/painel/descoberta` |
| GET | `/painel/maquinas/{id}/historico/{kind}/{report}` |
| GET | `/painel/maquinas/{id}/historico` |
| GET | `/painel/maquinas/{id}/mesclar` |
| GET | `/painel/padronizacao` |
| GET | `/painel/perguntas/{id}/csv` |
| GET | `/painel/perguntas/{id}` |
| GET | `/painel/perguntas` |
| GET | `/painel/scripts` |
| GET | `/painel/terminal` |

## Admin (ação) (77)

Quem pode: sessão de admin, só da própria origem. Sem sessão: 303 → /painel/entrar. Sessão de leitura: 403, com o evento acesso.negado na auditoria. De outra origem: 403.

| Método | Caminho |
|---|---|
| POST | `/painel/alertas/lote` |
| POST | `/painel/alertas/{id}/reconhecer` |
| POST | `/painel/autoatendimento/pedidos/{id}/aprovar` |
| POST | `/painel/autoatendimento/pedidos/{id}/recusar` |
| POST | `/painel/campanhas/arquivar-encerradas` |
| POST | `/painel/campanhas/nova` |
| POST | `/painel/campanhas/{id}/{op}` |
| POST | `/painel/configuracao/acesso/sessoes/usuario` |
| POST | `/painel/configuracao/acesso/sessoes/{id}/encerrar` |
| POST | `/painel/configuracao/acesso/testar-ad` |
| POST | `/painel/configuracao/acesso/testar-oidc` |
| POST | `/painel/configuracao/acesso/usuarios/{name}/remover` |
| POST | `/painel/configuracao/acesso/usuarios/{name}/senha` |
| POST | `/painel/configuracao/acesso/usuarios` |
| POST | `/painel/configuracao/agente/token/{id}/revogar` |
| POST | `/painel/configuracao/agente/token` |
| POST | `/painel/configuracao/aparencia/logo` |
| POST | `/painel/configuracao/autoatendimento` |
| POST | `/painel/configuracao/bios/senha-padrao/remover` |
| POST | `/painel/configuracao/bios/senha-padrao` |
| POST | `/painel/configuracao/descoberta` |
| POST | `/painel/configuracao/notificacoes/teste` |
| POST | `/painel/configuracao/parametros/restaurar` |
| POST | `/painel/configuracao/parametros` |
| POST | `/painel/configuracao/patrimonio` |
| POST | `/painel/descoberta/faixas/{id}/{op}` |
| POST | `/painel/descoberta/faixas` |
| POST | `/painel/descoberta/varrer` |
| POST | `/painel/grupos-implantacao/colar` |
| POST | `/painel/grupos-implantacao/devolver` |
| POST | `/painel/grupos-implantacao/faixa/remover` |
| POST | `/painel/grupos-implantacao/faixa` |
| POST | `/painel/grupos-implantacao/janela` |
| POST | `/painel/grupos-implantacao/maquinas` |
| POST | `/painel/grupos-implantacao/mover` |
| POST | `/painel/grupos-implantacao/nome` |
| POST | `/painel/grupos-implantacao/predio/excecoes` |
| POST | `/painel/grupos-implantacao/predio/remover` |
| POST | `/painel/grupos-implantacao/predio` |
| POST | `/painel/grupos-implantacao/{ring}/remover-janela` |
| POST | `/painel/inventario/grupo` |
| POST | `/painel/jobs/{id}/cancelar` |
| POST | `/painel/maquinas/{id}/anel` |
| POST | `/painel/maquinas/{id}/aposentar` |
| POST | `/painel/maquinas/{id}/atualizar` |
| POST | `/painel/maquinas/{id}/bios/configurar` |
| POST | `/painel/maquinas/{id}/bios/preparar` |
| POST | `/painel/maquinas/{id}/bios/{guarda}/gravar` |
| POST | `/painel/maquinas/{id}/bios/{guarda}/resolver` |
| POST | `/painel/maquinas/{id}/bios/{guarda}/revelar` |
| POST | `/painel/maquinas/{id}/buscar` |
| POST | `/painel/maquinas/{id}/https` |
| POST | `/painel/maquinas/{id}/ligar` |
| POST | `/painel/maquinas/{id}/mesclar` |
| POST | `/painel/maquinas/{id}/reiniciar` |
| POST | `/painel/maquinas/{id}/restaurar` |
| POST | `/painel/maquinas/{id}/terceiros/atualizar` |
| POST | `/painel/maquinas/{id}/terminal/abrir` |
| POST | `/painel/maquinas/{id}/terminal/comando` |
| POST | `/painel/maquinas/{id}/terminal/fechar` |
| POST | `/painel/maquinas/{id}/windows/instalar` |
| POST | `/painel/padronizacao/{id}/excluir` |
| POST | `/painel/padronizacao` |
| POST | `/painel/patch/aprovar-lote` |
| POST | `/painel/patch/aprovar` |
| POST | `/painel/patch/correcao` |
| POST | `/painel/patch/revogar` |
| POST | `/painel/patch/terceiros/aprovar-lote` |
| POST | `/painel/patch/terceiros/aprovar` |
| POST | `/painel/patch/terceiros/baixar` |
| POST | `/painel/patch/terceiros/excluir` |
| POST | `/painel/patch/terceiros/revogar` |
| POST | `/painel/perguntas` |
| POST | `/painel/scripts/{id}/configurar` |
| POST | `/painel/scripts/{id}/excluir` |
| POST | `/painel/scripts/{id}/executar` |
| POST | `/painel/scripts` |
