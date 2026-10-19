# Modelo de ameaças do patchd (Aula 20.1)

O patchd tem o poder que um atacante procura: executa como SYSTEM (ou root) em todas as
máquinas da frota. Este documento diz o que proteger, de quem, por onde cada um pode entrar, o
que já segura cada caminho e o que falta. É o mapa do bloco de segurança (S1 a S9): cada
lacuna tem um dono no plano do fim do documento.

Revisar este documento a cada aula que acrescente uma rota, um tipo de job, um segredo ou uma
fronteira nova. A matriz de rotas (docs/rotas.md) é gerada pelo teste e não precisa de revisão
à mão.

## 1. O que proteger (ativos)

| # | Ativo | Por que importa | Onde fica |
|---|---|---|---|
| A1 | **Chave de jobs** (Ed25519) | Quem assina jobs executa, como SYSTEM, os tipos de job que o agente aceita, inclusive instalar um executável | arquivo no servidor (`PATCHD_JOB_SIGNING_KEY_FILE`, segredo do compose) |
| A2 | **Chave de release** (Ed25519) | Quem assina uma versão do agente troca o próprio agente em toda a frota | fora do servidor e do repositório (`~/.patchd-release`, no host de quem publica) |
| A3 | **Sessão de admin do painel** | Aprova pacotes, cria campanhas e jobs (assinados pela A1), muda parâmetros e grupos | cookie `HttpOnly`/`SameSite=Strict`; a sessão guardada pelo hash |
| A4 | **Credencial de cada máquina** | Fala com a API em nome da máquina: manda inventário, recebe jobs dela | agente (arquivo protegido); servidor guarda só o hash |
| A5 | **Token de enrollment** | Registra máquinas novas na frota | mostrado uma vez; servidor guarda o hash, com prazo e usos |
| A6 | **Banco** | Inventário (usuários conectados, software: dado pessoal, LGPD), auditoria, aprovações, sessões | PostgreSQL; senha em arquivo |
| A7 | **Segredos de integração** | Senha do SMTP, segredo do cliente OIDC, senha do banco, token de métricas | arquivos (segredos do compose), nunca no banco nem na tela |
| A8 | **Disponibilidade das correções** | Sem o patchd, a frota fica sem atualização: o risco que ele existe para reduzir | servidor, catálogo e cache |

## 2. De quem (atores)

| Ator | Pode | Quer |
|---|---|---|
| T1 Atacante na rede interna | Alcançar a porta do servidor e das máquinas; observar tráfego sem TLS | Credenciais, sessão do painel, jobs forjados |
| T2 Usuário comum de uma máquina | Rodar programas na própria sessão, abrir a Central, ler arquivos que o Windows deixa | Virar administrador da máquina pelo agente; pedir o que a loja não oferece |
| T3 Máquina comprometida (agente adulterado) | Usar a credencial dela à vontade | Envenenar o painel (inventário falso, HTML), esgotar o servidor, pegar jobs de outras |
| T4 Conta do painel comprometida (senha ou sessão) | O que o perfil dela permite | Leitura: dados da frota. Admin: a frota inteira (A3 → A1) |
| T5 Fonte externa comprometida (catálogo, winget, CDN de instalador) | Mandar dados ou arquivos que o servidor baixa | Instalador adulterado distribuído pela frota |
| T6 Quem tem acesso ao servidor ou ao banco (operador, backup) | Ler arquivos e tabelas | As chaves e os segredos (A1, A7), o inventário (A6) |

## 3. Fronteiras de confiança

```
 [navegador do admin] --HTTPS + sessão--> [servidor patchd] <--HTTPS + credencial-- [agente (SYSTEM)]
                                             |      |  ^                                  |
                                   [PostgreSQL]  [segredos]   [fontes externas]     pipe local
                                                       (catálogo, winget, CDN)            |
                                                                                    [Central (usuário)]
 [host de quem publica] --release assinada--> [servidor] --> [agentes]
```

1. **Navegador ↔ painel**: tudo que chega é do usuário (formulários, URL, cookie).
2. **Agente ↔ servidor**: a máquina é dona da credencial, mas pode estar comprometida (T3); o
   servidor pode estar comprometido do ponto de vista do agente.
3. **Central ↔ agente** (pipe local): o usuário comum (T2) do lado de fora, SYSTEM do lado de
   dentro: a fronteira de elevação de privilégio.
4. **Servidor ↔ fontes externas**: dados e arquivos que entram no catálogo e no cache.
5. **Servidor ↔ banco e segredos**: quem lê o disco ou o dump (T6).
6. **Publicação ↔ frota**: a release do agente, assinada fora do servidor.

## 4. Ameaças e controles (STRIDE)

S: falsificar identidade · T: adulterar · R: negar o que fez · I: vazar · D: negar serviço ·
E: elevar privilégio. "Controle" diz o que já existe (com a aula); "lacuna" diz o que falta e
quem resolve.

### 4.1 Navegador ↔ painel

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| S | Senha adivinhada | Limite por usuário+IP e por IP (7.2); AD por LDAPS com bind do próprio usuário (7.3); RHBK com PKCE e nonce (9.4) | — |
| S | Sessão roubada | Cookie `HttpOnly`, `Secure` com TLS, `SameSite=Strict`; 8 h e 30 min parado; só o hash no banco (7.2) | Sem amarração ao IP ou ao navegador (aceitável; registrar) |
| T | Formulário de outro site (CSRF) | `CrossOriginProtection` em todo POST; `SameSite=Strict`; **matriz de rotas testada (20.1)** | — |
| T | Script injetado (XSS) | `html/template` com escape; CSP sem `script-src` (nenhum JavaScript roda) e `frame-ancestors 'none'` (7.2, 11.3) | Varredura dinâmica: **S5 (ZAP)** |
| R | Admin nega o que fez | Trilha de auditoria com antes → depois, origem e ID da requisição (15.13, 19.2); terminal também (18.1) | Trilha no mesmo banco que o admin influencia: **S9** (cópia para fora) |
| I | Leitura vendo o que é de admin | `adminAction` em toda a Configuração; **teste da matriz (20.1)** | — |
| E | Leitura fazendo o que é de admin | `sameOrigin(adminAction)` em toda ação; sessão de leitura recebe 403 e vira `acesso.negado` (15.13); **teste da matriz (20.1)** | — |
| E | Grupo tirado continua com o perfil | Sessões da origem encerradas quando um grupo sai da lista (19.4) | — |

### 4.2 Agente ↔ servidor

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| S | Servidor falso entregando jobs | TLS com a CA da instituição (`-ca-file`, 9.2); **jobs assinados (A1) e conferidos pelo agente**; releases assinadas (A2) | — |
| S | Máquina se passando por outra | A máquina é a dona da credencial: o ID vem da credencial, nunca da URL (4.3) | — |
| T | Job adulterado no caminho | Assinatura Ed25519 do job, prazo de validade, ID amarrado à máquina (8.1) | — |
| T | **Servidor comprometido assina job malicioso** | Tipos de job fechados; instalador conferido pelo SHA-256 do job; organização da assinatura Authenticode exigida quando o manifesto define | **F1** (abaixo): com a chave de jobs, instala qualquer executável. **S6/S8** |
| T | Inventário falso (T3) envenena o painel | Escape no HTML; limites de tamanho (8 MiB por corpo, listas) | Parsers sem fuzzing: **S3** |
| D | Máquina enchendo o servidor | Limite de corpo; gzip conferido | Sem limite de requisições por máquina: **S6** |
| I | Credencial vazada no tráfego | TLS; porta HTTP só para transição e cache (9.3) | Fechar a porta HTTP quando ninguém usar (já possível; registrar) |
| E | Token de enrollment reaproveitado | Prazo e número de usos; só o hash (4.2, 15.2) | Revisão no **S6** |

### 4.3 Central ↔ agente (pipe local)

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| S | Programa se passando pelo usuário | O agente lê a conta e os grupos do token do Windows de quem abriu o pipe (17.3); só sessão interativa; pipe fechado para a rede (SDDL) | — |
| E | Usuário pedindo o que a loja não oferece | O servidor refaz a loja a cada pedido; só versões aprovadas (17.2) | — |
| E | Usuário comum virando administrador pelo agente | Página local com segredo no caminho, Host e Origin conferidos (17.4, 17.5b) | **Permissões das pastas do agente** (instaladores baixados, cópia da Central, atualização): troca do arquivo entre a conferência e a execução. **S7** |
| D | Usuário enchendo o servidor de pedidos | Limite por usuário no pipe (5 pedidos e 60 consultas por 10 min) | — |

### 4.4 Servidor ↔ fontes externas

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| T | Instalador adulterado na origem (T5) | Download com SHA-256 do winget; organização Authenticode da regra (14.2); aprovação humana (14.5) | Regras sem `Signer` aceitam instalador sem assinatura conhecida: **F2** |
| T | Catálogo envenenado | Fontes oficiais por HTTPS; o catálogo só informa, não executa | Fuzzing dos parsers do catálogo: **S3** |
| D | Fonte fora do ar | Catálogo guardado; offline (wsusscn2) | — |

### 4.5 Servidor ↔ banco, segredos e processo

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| I | Segredo no log, na tela ou no banco | Segredos só em arquivo (`_FILE`); nunca por flag; o log e a auditoria não levam segredos (testes); parâmetros da tela sem segredo (19.2) | — |
| I | Dump do banco com dado pessoal | Retenção (10.5, 19.2) | Cópias do banco cifradas e com acesso restrito: **S9** |
| T | Dependência vulnerável | Poucas dependências; Go atualizado | **S2**: govulncheck e gosec a cada build |
| E | Porta de métricas | Porta separada, com token (10.2) | — |
| E | Proxy do cache de repositórios | Só as origens da lista (6.6) | Conferir que não vira proxy aberto: **S5** |

### 4.6 Publicação ↔ frota

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| T | Release adulterada | Chave de release fora do servidor; manifesto assinado conferido pelo agente; o agente só aceita versão mais nova (5.4, 15.x) | Rotação da chave: **S8** |

## 5. Achados desta revisão

- **F1 — A chave de jobs é a chave da frota.** Quem controla o servidor (ou só lê o arquivo da
  chave de jobs) assina um job `thirdparty` com um executável qualquer e o SHA-256 dele; sem
  `Signer` no job, o agente aceita um arquivo sem assinatura conhecida. Mitigação a desenhar no
  **S6/S8**: o agente exigir assinatura Authenticode de uma organização de uma lista que fica
  na máquina (fora do alcance do servidor), e a aprovação de um instalador assinada por uma
  segunda chave guardada fora do servidor (como a de release).
- **F2 — Regra de terceiros sem `Signer`.** O manifesto pode ter programas sem a organização
  da assinatura; para eles, só o SHA-256 vale. Revisar o manifesto e tornar o `Signer`
  obrigatório para executáveis (**S6**).
- **F3 — O `.bat` de senha de BIOS nas máquinas HP** (fora do patchd, achado na conversa da
  Aula 17): a senha fica legível por qualquer usuário da máquina. Tratar junto com o
  inventário da BIOS (Módulo 21): ACL por SID e migração para um cofre.

## 6. Plano do bloco de segurança

| | O quê | Aula |
|---|---|---|
| S1 | Este modelo de ameaças | 20.1 |
| S4 | Matriz de rotas gerada do código e testada na prática | 20.1 |
| S2 | govulncheck, gosec e staticcheck no script de build e num roteiro de CI | 20.2 |
| S3 | Fuzzing dos parsers: inventário, busca, BER/LDAP, JWT, gzip, CSV, imagem | 20.3 |
| S5 | ZAP (varredura dinâmica) contra o painel do lab, com sessão | 20.4 |
| S6 | API dos agentes: limite por máquina, F1 e F2, tokens de enrollment | 20.5 |
| S7 | Elevação local no agente: permissões das pastas, troca entre conferir e executar | 20.6 |
| S8 | Rotação das chaves de jobs e de release sem parar a frota | 20.7 |
| S9 | Pacote para revisão externa: SBOM, este documento, resultados, cópias do banco | 20.8 |
