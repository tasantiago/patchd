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

Sobre T6 no ambiente atual (Aula 20.2): o root do host do servidor é de uma equipe de
manutenção de servidores, com acesso registrado e gravado. É um controle de detecção, não de
prevenção: quem tem root lê a chave de jobs (A1). Cópias do banco ainda não existem (**F4**). A
chave de release (A2) existia numa máquina só, sem cópia (**F5**, resolvido na 20.7 com a chave
seguinte guardada offline).

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
| T | Script injetado (XSS) | `html/template` com escape; CSP sem `script-src` (nenhum JavaScript roda), sem `'unsafe-inline'` nem no CSS, e `frame-ancestors 'none'` (7.2, 11.3, 20.4); varredura do ZAP (20.4) | — |
| R | Admin nega o que fez | Trilha de auditoria com antes → depois, origem e ID da requisição (15.13, 19.2); terminal também (18.1) | Trilha no mesmo banco que o admin influencia: a cópia cifrada fora do host (F4, roteiro na seção 5f) |
| I | Leitura vendo o que é de admin | `adminAction` em toda a Configuração; **teste da matriz (20.1)** | — |
| E | Leitura fazendo o que é de admin | `sameOrigin(adminAction)` em toda ação; sessão de leitura recebe 403 e vira `acesso.negado` (15.13); **teste da matriz (20.1)** | — |
| E | Grupo tirado continua com o perfil | Sessões da origem encerradas quando um grupo sai da lista (19.4) | — |

### 4.2 Agente ↔ servidor

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| S | Servidor falso entregando jobs | TLS com a CA da instituição (`-ca-file`, 9.2); **jobs assinados (A1) e conferidos pelo agente**; releases assinadas (A2) | — |
| S | Máquina se passando por outra | A máquina é a dona da credencial: o ID vem da credencial, nunca da URL (4.3) | — |
| T | Job adulterado no caminho | Assinatura Ed25519 do job, prazo de validade, ID amarrado à máquina (8.1) | — |
| T | **Servidor comprometido assina job malicioso** | Tipos de job fechados; instalador conferido pelo SHA-256 do job; organização da assinatura exigida quando o manifesto define; **lista de organizações da própria máquina** (`-allowed-signers`, 20.5b); a chave de jobs gira sem parar a frota (20.7) | Máquina sem a lista; versão antiga de programa da lista: aprovação por segunda chave (desenho na seção 5e) |
| T | Inventário falso (T3) envenena o painel | Escape no HTML; limites de tamanho (8 MiB por corpo, listas); texto limpo na entrada e fuzzing (20.3) | — |
| D | Máquina enchendo o servidor | Limite de corpo; gzip conferido; **limite de requisições por máquina** (20.5: inventário e busca 20/h, o resto 720/h; 429 com Retry-After) | — |
| I | Credencial vazada no tráfego | TLS; porta HTTP só para transição e cache (9.3) | Fechar a porta HTTP quando ninguém usar (já possível; registrar) |
| E | Token de enrollment reaproveitado | Prazo e número de usos (padrão 24 h e 1 uso; máximo 30 dias e 10 mil); só o hash; 256 bits (adivinhar é inviável, não precisa de limite de tentativas) (4.2, 15.2, revisto na 20.5) | — |
| S | **Credencial de máquina copiada** (imagem de disco, backup do agente) | Pasta do agente só do SYSTEM/root (20.6); aposentar a máquina revoga a credencial | Não expira nem gira (fora da 20.7: aposentar e registrar de novo) |

### 4.3 Central ↔ agente (pipe local)

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| S | Programa se passando pelo usuário | O agente lê a conta e os grupos do token do Windows de quem abriu o pipe (17.3); só sessão interativa; pipe fechado para a rede (SDDL) | — |
| E | Usuário pedindo o que a loja não oferece | O servidor refaz a loja a cada pedido; só versões aprovadas (17.2) | — |
| E | Usuário comum virando administrador pelo agente | Página local com segredo no caminho, Host e Origin conferidos (17.4, 17.5b); pasta de dados com dono Administradores, nascida protegida, sem links (20.6); instalador seguro da conferência ao fim da execução e conferido de novo; TEMP do instalador numa pasta do SYSTEM (20.6) | — |
| D | Usuário enchendo o servidor de pedidos | Limite por usuário no pipe (5 pedidos e 60 consultas por 10 min) | — |

### 4.4 Servidor ↔ fontes externas

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| T | Instalador adulterado na origem (T5) | Download com SHA-256 do winget; organização Authenticode da regra (14.2); aprovação humana (14.5); regra sem `signer` exige `unsigned_reason`, à vista na aprovação (20.5) | O 7-Zip não assina: vale o SHA-256 do winget e a aprovação |
| T | Catálogo envenenado | Fontes oficiais por HTTPS; o catálogo só informa, não executa; fuzzing do YAML do winget e do metalink (20.3); os JSON das fontes passam pelo `encoding/json` | — |
| D | Fonte fora do ar | Catálogo guardado; offline (wsusscn2) | — |

### 4.5 Servidor ↔ banco, segredos e processo

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| I | Segredo no log, na tela ou no banco | Segredos só em arquivo (`_FILE`); nunca por flag; o log e a auditoria não levam segredos (testes); parâmetros da tela sem segredo (19.2) | — |
| I | Dump do banco com dado pessoal | Retenção (10.5, 19.2); cópia cifrada com age, chave privada fora do servidor (10.3) | Ligar a cifra em produção: F4 (seção 5f) |
| T | Dependência vulnerável | Poucas dependências; Go na versão exata (Dockerfiles e go.mod); `scripts/seguranca.sh` e o CI semanal (20.2) | — |
| D | Banco perdido sem cópia | `backup.sh` e timer (10.1); ensaio `restaurar.sh --testar` (10.3); **Configuração › Sistema › Cópias do banco** mostra a última cópia e o último ensaio (20.8) | Ligar em produção: **F4** (seção 5f) |
| E | Porta de métricas | Porta separada, com token (10.2) | — |
| E | Proxy do cache de repositórios | Só as origens da lista, só GET/HEAD na porta 80, CONNECT recusado; testes do repocache (6.6) | — |

### 4.6 Publicação ↔ frota

| | Ameaça | Controle | Lacuna |
|---|---|---|---|
| T | Release adulterada | Chave de release fora do servidor; manifesto assinado conferido pelo agente; o agente só aceita versão mais nova (5.4, 15.x); a chave gira sem parar a frota (20.7) | — |
| D | Chave de release perdida | **Chave seguinte** embutida nos agentes, com a privada só offline (20.7) | — |

## 5. Achados desta revisão

- **F1 — A chave de jobs é a chave da frota.** Quem controla o servidor (ou só lê o arquivo da
  chave de jobs) assina um job `thirdparty` com um executável qualquer e o SHA-256 dele; sem
  `Signer` no job, o agente aceita um arquivo sem assinatura conhecida. **Mitigado na 20.5b**:
  com `-allowed-signers` no serviço do agente, ele só executa instalador com assinatura válida
  de uma organização da lista, diga o job o que disser; a lista fica na linha de comando do
  serviço (só um administrador da máquina muda) e aparece no resumo da máquina no painel.
  Resta: a lista é opcional (máquina sem ela continua como antes, e o painel avisa), e quem
  tem a chave de jobs ainda instala a versão que quiser de um programa assinado por uma
  organização da lista (inclusive uma versão antiga, com falha conhecida). Para isso: a
  aprovação de um instalador assinada por uma segunda chave fora do servidor (desenho na
  seção 5e, sem aula marcada).
- **F2 — Regra de terceiros sem `Signer`.** O manifesto pode ter programas sem a organização
  da assinatura; para eles, só o SHA-256 vale. **Resolvido na 20.5**: toda regra de instalação
  tem `signer` ou `unsigned_reason`; sem os dois, o manifesto não carrega. No manifesto
  embutido, só o 7-Zip fica sem assinatura (ele não assina o instalador), com o motivo, que a
  tela de aprovação mostra.
- **F3 — O `.bat` de senha de BIOS nas máquinas HP** (fora do patchd, achado na conversa da
  Aula 17): a senha fica legível por qualquer usuário da máquina. Tratar junto com o
  inventário da BIOS (Módulo 21): ACL por SID e migração para um cofre. **Na 21.1**: o
  inventário passou a dizer, máquina a máquina, se a senha de administrador da BIOS está
  definida (HP, Dell e Lenovo, pela interface do fabricante); é como conferir o que o `.bat`
  fez e o que falta, antes de aposentá-lo. **Na 21.2**: o cartão "BIOS sem senha" em
  Inventário conta a frota e filtra a lista (e o CSV); é a medida do F3.
- **F4 — Sem cópia do banco** (Aula 20.2, resposta do lab da 20.1). Perder o disco do
  servidor é perder aprovações, auditoria e histórico; a frota continua de pé (o agente não
  depende do banco para funcionar), mas sem trilha. As ferramentas já existem
  (`deploy/backup.sh`, o timer do systemd, a cifragem com `age` e o ensaio de restauração:
  Aulas 10.1 e 10.3, em `docs/operacao.md`); falta ligar no servidor de produção: o timer, as
  chaves públicas em `backup_recipients`, a cópia para fora do host e o ensaio mensal. A cópia
  leva dado pessoal (A6) e os hashes de sessão: o acesso a ela é de T6. **Na 20.8**: o painel
  passou a mostrar a última cópia e o último ensaio de restauração (vermelho sem cópia ou com
  ensaio que não conferiu; amarelo com cópia velha, sem cifra, sem ensaio ou ensaio velho), e o
  roteiro de produção está na seção 5f. Fica aberto até o roteiro ser cumprido no servidor.
- **F5 — A chave de release existe numa máquina só, sem cópia** (Aula 20.2). Perder a chave
  impede publicar versões: os agentes só aceitam essa chave, e trocar de chave exige uma
  release assinada pela antiga. Comprometer a máquina de desenvolvimento compromete a frota.
  Ações: (1) duas cópias offline cifradas (dois pendrives, guardados em lugares diferentes,
  por exemplo um cofre), com a frase de acesso fora do pendrive; (2) **feito na 20.7**: o
  agente aceita duas chaves (a atual e a seguinte); a seguinte é gerada já, guardada só
  offline e embutida no build, de modo que perder ou vazar a atual se resolve com uma versão
  assinada pela seguinte (seção 5e).

- **F6 — Pasta de dados do agente criada antes do install** (Aula 20.6, revisão do S7). Um
  usuário comum podia criar `C:\ProgramData\patchd` antes, ficar dono e, depois do install,
  dar a si mesmo controle total da pasta onde os instaladores esperam para rodar como SYSTEM.
  **Resolvido na 20.6** (seção 5d).
- **F7 — Login pelo RHBK bloqueável sem autenticação** (Aula 20.4b, varredura ativa).
  **Resolvido** (seção 5c).
- **F8 — Texto inválido do pedido chegando ao banco**: tentativa negada fora da trilha e 500
  num formulário (Aula 20.4b, varredura complementar). **Resolvido** (seção 5c).

## 5a. Análise estática (S2, Aula 20.2)

`scripts/seguranca.sh` roda govulncheck, staticcheck e gosec, em versões fixas, para linux,
windows e darwin, e sai com erro se algum achar algo. O CI (`.github/workflows/seguranca.yml`)
roda o mesmo script a cada push e toda segunda.

Primeira passada (Go 1.27.1):

- **govulncheck**: 12 vulnerabilidades da biblioteca padrão alcançadas pelo código (net/http,
  HTTP/2, textproto, crypto/tls, html/template), todas corrigidas no Go 1.27.2. Correção: a
  versão exata nos dois Dockerfiles e `go 1.27.2` no go.mod. Os agentes só recebem a correção
  numa release nova (v0.17.7). Uma 13ª (`os.Root` no Windows) estava num pacote importado, mas
  em função não chamada: o govulncheck a lista à parte e não falha por ela.
- **govulncheck, no container com rede**: mais 2 no `golang.org/x/text` v0.29.0
  (GO-2026-6629 e GO-2026-5970), que entra pelo pgx (normalização da senha do banco). Caminho
  só do servidor. Atualizado para v0.43.0 (com `golang.org/x/sync` v0.24.0). Onde as
  dependências são cópias locais (`replace`), o govulncheck não sabe a versão do módulo e não
  as compara: a análise que vale é a do ambiente com as versões reais e a do CI.
- **staticcheck**: código morto que dependia do SO (funções de um sistema só em arquivo comum),
  dois valores atribuídos e nunca lidos em testes, mensagens de erro com maiúscula. Os `| 0`
  do BER ficam, com `//lint:ignore` e o motivo.
- **gosec**: 179 achados no Linux e mais 74 só no Windows. Corrigidos: a ocupação do disco
  com livre > total (vinha do agente: T3), o escape `\UFFFFFFFF` no YAML do winget, o
  `uint64` do WMI acima do `int64`, os dias da janela do anel, o `-` no começo de um nome de
  conta no macOS, os ícones da Central montados sem `template.HTML`.

Regras da casa para exceções:

1. Toda exceção é `// #nosec Gxxx -- motivo` na linha (ou no comando de várias linhas). Sem a
   regra ou sem o motivo, o gosec recusa a anotação (`nosec-require-*` no `.gosec.json`).
2. Erro ignorado de propósito é `_ = f.Close()`, à vista. Ficam fora do G104 só o `os.Remove`
   de limpeza, as escritas num `bufio.Writer` (o erro volta no `Flush`) e o `Flush` do
   `tabwriter` dos comandos de terminal.
3. G304 (arquivo aberto por caminho variável) fica desligada: dispara em todo caminho vindo de
   configuração. O caminho vindo de fora (requisição) é a G703, que fica ligada.
4. G103 (`unsafe`) fica desligada só nos arquivos `_windows.go`: a API do Windows exige.
5. Ferramenta nova ou versão nova é um commit só para isso, com os achados que ela trouxer.

## 5b. Fuzzing (S3, Aula 20.3)

`scripts/fuzz.sh` roda cada alvo `FuzzXxx` do código (a lista sai do código: alvo novo entra
sozinho) por `FUZZTIME` (30 s por padrão). O CI roda toda segunda, 1 minuto por alvo. Uma
entrada que quebra um alvo fica em `testdata/fuzz/<Alvo>/` do pacote e vira teste de
regressão: o `go test` comum roda todas.

| Alvo | De onde vem a entrada | Propriedade | Achado |
|---|---|---|---|
| `store.FuzzSaveInventory` | Agente (T3) | Depois da limpeza da entrada, o PostgreSQL grava | **Sim**: byte 0xff e NUL faziam o INSERT falhar; o inventário virava 500 a cada envio |
| `api.FuzzDecodeJSON` | Agente, puro ou gzip | Nunca 5xx; nenhum NUL passa | Guarda a correção acima |
| `api.FuzzConsultas` | URL do painel | Filtros em UTF-8 válido, sem NUL, no limite | **Sim**: `%C3` (meio "ç") ia para a tela e, na Auditoria, para o banco (500) |
| `texto.FuzzClip` | — | Corte sem partir caractere | Correção nova (`internal/texto`) |
| `repocache.FuzzMetalink` | Fedora (T5) | Espelho é base https terminada em "/" | **Sim** (pela semente, não pela mutação): `?x` ou `#` no endereço do espelho |
| `ber.FuzzParse` | Servidor LDAP | Sem pânico; remontado decodifica igual | Não |
| `ldap.FuzzRespostas` | Servidor LDAP | Bind e busca terminam sem pânico | Não |
| `oidc.FuzzIDToken` | Navegador (volta do RHBK) | Só o token assinado passa; ≤ 1 leitura da JWKS | Não |
| `oidc.FuzzIDTokenClaims` | RHBK com defeito | O que passa cumpre emissor, audiência, prazo, nonce | Não |
| `export.FuzzTable` | Agente → planilha | Relê igual; nenhuma célula começa como fórmula | Não (o primeiro "achado" era a propriedade exigente demais com o `\r`) |
| `ui.FuzzCheckLogo` | Admin, API da Central | Aceito = PNG/JPEG nos limites | Não |
| `winget.FuzzParseInstaller` | GitHub do winget (T5) | Sem pânico | Não |
| `version.FuzzCompare` | Agente e fontes | Comparação é uma ordem (a = a; trocar lados troca o sinal) | Não |

A correção dos dois primeiros é uma só, na entrada: `internal/texto` troca UTF-8 inválido e NUL
por U+FFFD (a mesma troca que o `encoding/json` já faz com UTF-8 inválido), e o `decodeJSON`
aplica a todo texto do corpo. Os cortes de texto (`s[:n]`) viraram `texto.Clip`: o detalhe do
resultado do job (2000 bytes, com texto em português, partia o "ç" e o banco recusava o
resultado), as buscas, o usuário da Auditoria, os grupos da prévia da loja e a descrição de
erro do RHBK.

Lacuna conhecida: os formulários do painel (POST) não passam pela mesma limpeza. Um admin que
mande UTF-8 inválido de propósito recebe um 500 na própria tela; não afeta outra pessoa.

## 5c. Varredura dinâmica (S5, Aula 20.4)

Antes do ZAP, o que ele confere de forma passiva virou regra do servidor e teste:

- **Cabeçalhos em toda resposta** (`api.SecurityHeaders`): `nosniff`, `X-Frame-Options: DENY`,
  `Referrer-Policy`, `Permissions-Policy`, `Cross-Origin-Opener/Embedder/Resource-Policy`,
  CSP padrão fechada e `Cache-Control: no-store`. Antes, só as páginas tinham; o JSON da API,
  os erros e os redirecionamentos saíam sem nada. Quem precisa de outro valor troca (a página,
  com a CSP dela; o CSS e o ícone, com cache).
- **CSP das páginas sem `'unsafe-inline'`**: o CSS saiu das páginas para `/painel/estilo.css`
  (com o hash na URL e cache longo), e as larguras das barras viraram classes (`.w0` a
  `.w100`). Atributo `style` e `<style>` na página quebram o teste.
- `TestCabecalhosDeSeguranca` chama todas as rotas do `api.go` (sem nada e com a sessão certa)
  e confere tudo isso; rota nova entra sozinha.

O ZAP (`scripts/zap.sh`, no host, contra o lab) roda com uma sessão aberta pela linha de
comando (`patchd-server session create -role leitura`, origem "varredura", prazo curto, na
trilha). `zap/regras.tsv` diz o que é FAIL (o que o teste acima garante), o que é IGNORE (com
o motivo: o anti-CSRF por cabeçalho e não por token, as datas na tela, as informativas) e o
resto é WARN. O spider não envia formulários POST: as ações ficam com a matriz de rotas e o
fuzzing.

A Central de software (no agente, porta local) também saiu do `'unsafe-inline'` na Aula 20.6
(v0.17.9): o CSS vem de `estilo.css`, sob o mesmo segredo no caminho.

### Resultado do lab (Aula 20.4b)

Duas rodadas: a passiva, com sessão (60 rotas), e a ativa, que rodou **sem login**: a sessão
tinha vencido (30 min parada) e o ZAP só achou a tela de login (11 rotas). Nenhum alerta alto.

| Alerta | Rodada | Decisão |
|---|---|---|
| Absence of Anti-CSRF Tokens (médio) | as duas | falso positivo: `CrossOriginProtection` em todo POST, inclusive o login, e cookie `SameSite=Strict`; a matriz de rotas testa (20.1). Já era IGNORE |
| Proxy Disclosure (médio) | ativa | falso positivo: não há proxy; TRACE, TRACK e OPTIONS recebem o mesmo 405 com qualquer `Max-Forwards`, sem `Server` (conferido à mão). IGNORE |
| Private IP Disclosure (baixo) | passiva | o inventário mostra o IP das máquinas, só com login: é a função da tela. IGNORE |
| User Controllable HTML Element Attribute (info) | passiva | os filtros só voltam se forem um valor da lista; o resto é descartado. IGNORE |
| Re-examine Cache-control, Non-Storable, Storable and Cacheable (info) | as duas | `no-store` nas páginas (basta); cache longo só no CSS, no ícone e no logotipo, públicos e com o hash na URL. IGNORE |
| Cookie Slack Detector (info) | ativa | era o sinal de que a sessão não valia na ativa. IGNORE; o zap.sh agora confere |
| **2 % de respostas 5xx** | ativa | **achado F7** (abaixo) |

O 5xx levou a duas correções e a uma varredura complementar, com sessão de leitura e de
admin, de todos os parâmetros de GET e campos de POST do painel com NUL, UTF-8 inválido,
aspas, travessia de caminho e textos longos (perto de 17 mil pedidos):

- **F7 — Login pelo RHBK bloqueável sem autenticação.** O início do login (`/painel/oidc/entrar`,
  público) guarda o login em andamento por 10 minutos, com um teto de mil; cheio, recusava
  com 503. Mil pedidos de qualquer um bloqueavam todo login pelo RHBK por 10 minutos (a
  varredura ativa fez isso). **Resolvido**: no máximo 10 por IP (sai o mais velho daquele IP)
  e, com a tabela cheia, sai o mais velho de todos, com aviso no log uma vez por minuto;
  ninguém é recusado. Quem enche só derruba os próprios logins, ou precisa de centenas de
  endereços para atrapalhar quem está no provedor naquele minuto, e quem perdeu o login
  clica de novo. Atrás de um encaminhamento que esconde o IP de origem (Docker Desktop), todos
  parecem um IP só: aí o limite por IP vira o geral de 10, o que ainda não recusa ninguém.
- **F8 — Texto inválido do pedido chegando ao PostgreSQL.** NUL ou UTF-8 inválido na URL ou
  num formulário ia cru para o banco, que recusa. Duas consequências: o "acesso negado" de
  uma sessão de leitura numa rota de admin com `%00` no caminho **não entrava na trilha**
  (repúdio), e o "revogar aprovação" dava 500. **Resolvido** em dois lugares: a trilha limpa
  todo campo antes de gravar (o NUL vira "�"; vale também para a linha de comando), e todo
  formulário do painel com NUL ou UTF-8 inválido, em qualquer campo ou na URL, recebe 400
  antes do handler (`checkFormText`, dentro do `sameOrigin`). Depois disso, a varredura
  complementar terminou sem nenhum 5xx e sem nenhum erro no log.

O `scripts/zap.sh` passou a conferir a sessão antes (sessão que não abre o painel: nem
começa) e depois (caiu no meio: sai com 4), e deixa o "Sair" fora da varredura. A ativa com
sessão veio na rodada seguinte (Aula 20.4c).

**Ativa com sessão (Aula 20.4c).** Sessão válida do começo ao fim, 73 rotas (contra 11 da
primeira ativa), 83 % de respostas 2xx e **nenhuma 5xx**. Os ataques ativos (injeção de SQL,
XSS refletido e persistente, travessia de caminho, inclusão de arquivo, injeção de comando,
entre outros) não acharam nada. Nenhum alerta novo: os seis que vieram são os da tabela acima,
já decididos. O "Private IP Disclosure" apareceu também na aba Hardware da máquina, que mostra
o gateway da placa de rede: mesma decisão (dado da máquina, só com login). Atenção para o
pacote de revisão (S9): o relatório do lab tem os endereços das VMs e da rede delas; só vai com
`ZAP=` se a lista de termos proibidos cobrir esses prefixos, e o `revisao.sh` barra se não
cobrir. **S5 fechado.**

## 5d. Elevação local de privilégio (S7, Aula 20.6)

O agente roda como SYSTEM e executa instaladores que ficam na pasta de dados
(`C:\ProgramData\patchd`): quem escreve nela vira SYSTEM. A revisão achou:

- **Pasta criada antes do install (achado F6).** O Windows deixa qualquer usuário criar pastas
  em `C:\ProgramData`. Quem criasse `patchd` antes do install ficava dono dela, e o dono
  sempre pode trocar a DACL, por mais protegida que esteja: bastava esperar o install e se dar
  controle total, ou plantar uma junção em `pacotes` apontando para uma pasta dele. Correção:
  a pasta nova nasce já com o descritor (dono Administradores, DACL protegida); se já existe,
  não pode ser link nem junção (o install recusa), o dono passa a ser Administradores e tudo
  dentro volta a herdar só dela (links e junções lá dentro são apagados). Cada correção sai no
  log ("pasta de dados corrigida na partida"). No Linux e no macOS, o mesmo com root.
- **Troca entre conferir e executar.** O instalador era conferido (SHA-256 e assinatura) e
  depois executado pelo caminho. Agora o agente segura o arquivo aberto, sem deixar ninguém
  escrever, renomear nem apagar (nem o SYSTEM), confere de novo e só solta depois que o
  instalador termina.
- **TEMP do instalador.** Rodando como SYSTEM, os instaladores extraíam os arquivos deles em
  `C:\Windows\Temp`, onde usuários comuns também criam arquivos (a porta clássica de troca de
  DLL). Agora o TEMP e o TMP do instalador são `pasta de dados\tmp\job-N`, só do SYSTEM.

Revistos, sem mudança: o executável do serviço e a cópia da Central ficam em `Program
Files\patchd` (só administradores escrevem); a atualização do agente e o catálogo offline
ficam na pasta de dados (agora protegida como acima); o canal local da Central tem a sua
própria ACL (17.3).

## 5e. Rotação de chaves (S8, Aula 20.7)

As duas chaves que a frota conhece (jobs, A1; release, A2) eram trocáveis só com a frota
inteira parada: o agente aceitava uma chave de cada, e a troca exigia que todas as máquinas
recebessem um agente novo **e** que o servidor mudasse a chave no mesmo instante. Agora:

- **Duas chaves de cada tipo no agente**: a atual e a seguinte, embutidas no build. Um job ou
  um manifesto vale se qualquer uma delas o assinou; as outras conferências (máquina, prazo,
  número do job, versão mais nova) valem igual para as duas.
- **A frota informa o que aceita**: o estado da máquina leva as impressões (12 hexadecimais)
  das chaves aceitas, e Configuração › Sistema › Chaves na frota conta. É o que diz quando o
  passo seguinte da troca é seguro (roteiro em `docs/operacao.md`).
- **Travas no build e na assinatura**: as quatro chaves do build são diferentes entre si; o
  `patchd-release sign` não assina executáveis que não confiam na chave que assina.
- **F5**: a chave seguinte de release existe desde já, só offline. Perder a atual não para as
  atualizações; vazar a atual se responde com uma versão assinada pela seguinte que não aceita
  mais a vazada (até a frota atualizar, a vazada só serve a quem também controla o servidor,
  que é quem entrega as versões).

Fica de fora, por decisão: a credencial de cada máquina (não gira; aposentar e registrar de
novo resolve o caso de cópia) e a **aprovação com segunda chave** (F1). Desenho registrado
para quando for necessário: o servidor guarda, para cada instalador aprovado, uma aprovação
(SHA-256, programa, versão) assinada por uma chave que fica fora do servidor, e o agente,
com a pública dela embutida, exige essa aprovação além do job. Custa um passo manual em cada
aprovação; hoje a lista de organizações (20.5b) e os tipos de job fechados cobrem a maior
parte do risco.

## 5f. Pacote para revisão externa (S9, Aula 20.8)

Quem revisa de fora recebe um arquivo só, gerado por `scripts/revisao.sh VERSÃO` a partir de
um commit e do build dele (uso em `docs/operacao.md`):

- **Código** do commit (`git archive`, sem o histórico do git).
- **SBOM** de cada executável, em CycloneDX 1.5 (`cmd/sbom`): o que o próprio executável
  gravou no build (módulos e versões, hash do go.sum, versão do Go, opções), sem ferramenta de
  fora. Replace por pasta local aparece como tal, sem a pasta. Determinístico: o mesmo
  executável dá o mesmo documento. O `cmd/sbom` recusa executável de outro commit ou
  compilado com alterações não commitadas.
- **Resultados**: `seguranca.sh` (S2), `go test` (com ou sem PostgreSQL, dito no pacote),
  `fuzz.sh` (S3) e, quando houver, o relatório do ZAP (S5).
- **Este documento** e `docs/rotas.md` (S1 e S4).

**Nada do ambiente sai.** Antes de fechar o arquivo, o script procura em tudo o que vai (o
código inclusive) os termos de uma lista que fica fora do repositório (domínio, nomes de host,
prefixo dos IPs internos, valores do `.env`), o conteúdo dos arquivos de segredo e qualquer
chave privada. Achou: diz o arquivo e de onde veio o termo, nunca o valor, e não fecha. O
histórico do git não vai, mas o script conta em quantos commits cada termo aparece: o nome de
computador que está no histórico continua uma decisão em aberto (reescrever o histórico).

### Roteiro de F4 no servidor de produção

1. `deploy/secrets/backup_recipients` com as duas chaves públicas do age (operacao.md, Cópias
   cifradas); as privadas fora do servidor, em dois lugares.
2. `deploy/systemd/patchd-backup.timer` ligado (`systemctl enable --now`), com o
   `WorkingDirectory` ajustado.
3. Cópia para fora do host: o destino é da equipe de servidores (outro host, compartilhamento
   com backup próprio); as cópias já saem cifradas. O servidor não enxerga este passo: o
   registro é a data do primeiro arquivo no destino.
4. Ensaio mensal com `restaurar.sh --testar` e a chave privada só durante o ensaio.
5. Configuração › Sistema › Cópias do banco sem amarelo nem vermelho: cópia de menos de 36 h,
   cifrada, e ensaio de menos de 35 dias que conferiu.

## 6. Plano do bloco de segurança

| | O quê | Aula |
|---|---|---|
| S1 | Este modelo de ameaças | 20.1 |
| S4 | Matriz de rotas gerada do código e testada na prática | 20.1 |
| S2 | govulncheck, gosec e staticcheck em `scripts/seguranca.sh` e no CI (feito: seção 5a) | 20.2 |
| S3 | Fuzzing dos parsers: inventário, busca, BER/LDAP, JWT, gzip, CSV, imagem (feito: seção 5b) | 20.3 |
| S5 | ZAP (varredura dinâmica) contra o painel do lab, com sessão (feito: seção 5c; F7 e F8 resolvidos na 20.4b; ativa com sessão limpa na 20.4c) | 20.4, 20.4b e 20.4c |
| S6 | API dos agentes: limite por máquina, F2 e tokens (20.5); F1 com a lista de organizações no agente (20.5b, v0.17.8) | 20.5 e 20.5b |
| S7 | Elevação local no agente: permissões das pastas, troca entre conferir e executar (feito: seção 5d, v0.17.9) | 20.6 |
| S8 | Rotação das chaves de jobs e de release sem parar a frota; F5 (feito: seção 5e, v0.17.10) | 20.7 |
| S9 | Pacote para revisão externa: SBOM, este documento, resultados (feito: seção 5f); cópias do banco no painel e roteiro (F4) | 20.8 |
