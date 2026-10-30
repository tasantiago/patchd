# Senha de administrador da BIOS pelo patchd (Aula 21.3)

Este documento desenha o substituto do `.bat` de senha da BIOS (achado F3 em
`docs/seguranca.md`). É a primeira escrita no firmware do Módulo 21: um erro aqui deixa a
máquina com uma senha que ninguém sabe, e a única saída é a assistência do fabricante. Por
isso o desenho vem antes do código, e as decisões do fim são de quem opera a frota.

Nenhum valor real (senha, nome de máquina, modelo exato do parque) entra neste arquivo.

## 1. O problema do `.bat`

- A senha fica **legível** na máquina: no arquivo, na linha de comando do processo e, às
  vezes, no histórico da GPO ou na pasta de onde o script rodou.
- É **uma senha para a frota inteira**: quem a lê numa máquina entra na BIOS de todas.
- Não há **registro** de quem definiu, quando, nem de quais máquinas ficaram sem.
- Trocar a senha exige outro `.bat` com a senha velha e a nova, o mesmo problema duas vezes.

O inventário já mede o tamanho do problema (Aulas 21.1 e 21.2: cartão "BIOS sem senha" e o
filtro na lista). Falta definir e trocar a senha sem os quatro pontos acima.

## 2. O que a Aula 21.3 já entrega: a caixa de cada máquina

Cada agente (v0.18.2 em diante) cria, na primeira partida, um par de chaves só seu
(`internal/machinebox`):

- **KEM**: ML-KEM-768 + X25519 (híbrido; resiste a um computador quântico futuro e,
  enquanto isso, não depende só do algoritmo novo). **KDF**: HKDF-SHA256. **AEAD**:
  ChaCha20-Poly1305. É o HPKE da biblioteca padrão do Go (RFC 9180), sem código de
  criptografia próprio.
- A **privada** fica em `chave-cifragem.key`, na pasta de dados, com a mesma proteção da
  credencial: gravação atômica, só SYSTEM e Administradores (Windows) ou só root (Linux),
  e reaplicada pela varredura da pasta (Aula 20.6).
- A **pública** vai no estado de cada check-in (`box_key`). O painel mostra a impressão curta
  (12 hexadecimais) no resumo da máquina, em "Chave de cifragem".
- Cada mensagem fechada leva um **propósito** (`bios-senha`) amarrado na derivação da
  chave: um envelope feito para a BIOS não abre como outra coisa, nem o contrário.

Com isso, o servidor pode mandar um segredo que **só aquela máquina abre**, por dentro de um
job assinado (Aula 8.1). O servidor, o banco, a cópia do banco e o log só veem o envelope.

## 3. O fluxo (guarda na Aula 21.4, gravação na 21.5)

### 3.1 Uma senha por máquina

A recomendação é uma senha **diferente em cada máquina**, sorteada pelo servidor, no modelo
do LAPS da Microsoft. Vazou a senha de uma máquina, vazou uma máquina.

O alfabeto evita a armadilha do teclado: na tela da BIOS o teclado é sempre o americano, e no
ABNT2 os símbolos mudam de lugar. Por isso só letras minúsculas e números, sem os
ambíguos (`0`/`o`, `1`/`l`/`i`): 31 símbolos. Com 12 caracteres são cerca de 59 bits, folga
grande para uma senha que só serve com a mão no teclado da máquina. O limite de 12 cabe nos
ThinkPads mais antigos; HP e Dell aceitam mais (decisão D3).

### 3.2 Guarda antes de aplicar (duas fases)

A senha nova é **guardada antes** de ir para a máquina. A ordem inversa (aplicar e depois
guardar) perde a senha se o servidor cair no meio.

1. O servidor sorteia a senha e grava, numa tabela própria, o envelope para a chave de
   guarda (seção 3.3). Estado: `pendente`. (Feito na 21.4: como a guarda ficou no servidor,
   o envelope da caixa da máquina é feito na hora do job, com a chave da caixa mais recente.)
2. Um job `bios_password` (tipo novo, lista fechada no agente) leva o envelope da máquina e,
   na troca, o envelope da senha atual. O job é assinado pela chave de jobs, como os outros.
3. O agente abre os envelopes **só na memória**, chama o método do fabricante (seção 4) e
   apaga os bytes ao terminar. Nada vai para o disco, para a linha de comando nem para o log.
4. O agente responde `aplicada` ou o código de erro do fabricante. O servidor passa a senha
   para `ativa` e a anterior para `substituída`.
5. **Sem resposta** (queda, rede): estado `incerta`. As duas senhas continuam guardadas, e o
   painel mostra as duas na revelação até um inventário seguinte conferir qual vale.

O agente tenta **uma vez** por job. Senha errada repetida trava a BIOS em vários modelos
(HP pede reinício depois de algumas tentativas), então não há laço de nova tentativa.

### 3.3 A chave de guarda (escrow)

A senha precisa ser recuperável pela TI, senão a primeira ida à BIOS vira chamado no
fabricante. Duas formas (decisão D1):

- **(a) Guarda no servidor**: um par HPKE de guarda, com a privada em arquivo de segredo do
  compose (como a chave de jobs). O painel tem "Revelar senha da BIOS", só admin, com motivo
  obrigatório, auditoria (`bios.senha.revelar`: quem, quando, qual máquina, o motivo) e aviso
  por e-mail. Prático no dia a dia; quem tomar o servidor lê as senhas.
- **(b) Guarda fora do servidor**: o servidor tem só a **pública** de guarda. Revelar é um
  comando no host de quem guarda a privada (como a assinatura de versões, Aula 20.7), que
  abre o envelope tirado do painel. Quem tomar o servidor não lê nenhuma senha; a revelação
  fica mais lenta e depende de uma pessoa.

A recomendação era **(b)** se a ida à BIOS fosse rara (formatação, troca de placa), e **(a)** se
o suporte de campo precisasse da senha toda semana. **Decidido: (a)**, feito na Aula 21.4 (seção 7).

### 3.4 A primeira troca: a senha atual da frota

Para trocar uma senha da BIOS, o fabricante exige a senha atual. As máquinas que passaram
pelo `.bat` têm a senha da frota. Na primeira rotação:

- o admin digita a senha atual da frota **uma vez**, num formulário do painel;
- o servidor a fecha para a caixa de cada máquina alvo, coloca o envelope no job e descarta o
  texto. Ela não vai para o banco aberta, nem para o log, nem para a auditoria (que registra
  só "senha atual informada para N máquinas");
- máquinas com `admin_password = false` no inventário recebem o job sem a senha atual (é uma
  definição, não uma troca).

Depois da primeira rotação, a senha atual de cada máquina é a que está na guarda, e o painel
não pede mais nada.

### 3.5 Quem pode o quê

| Ação | Quem | Registro |
|---|---|---|
| Ver se a máquina tem senha, interface e Secure Boot | leitura e admin | — |
| Definir ou trocar (uma máquina, um grupo, a frota) | admin | `bios.senha.job` com a lista de máquinas |
| Revelar a senha de uma máquina | admin (D1-a) ou quem guarda a chave (D1-b) | `bios.senha.revelar` com o motivo |
| Remover a senha | ninguém pelo painel (D5) | — |

## 4. Os métodos de cada fabricante

O inventário (Aula 21.1) já diz qual interface cada máquina tem. A escrita usa a mesma.

| Fabricante | Classe e método (WMI) | Observações |
|---|---|---|
| HP | `root\HP\InstrumentedBIOS`, `HP_BIOSSettingInterface.SetBIOSSetting("Setup Password", nova, atual)` | Senhas com o prefixo `<utf-16/>`. **HP Sure Admin** (modo de autenticação reforçado) recusa a troca por senha e exige um pacote assinado pelo certificado da organização: a máquina nesse modo fica fora (D4). |
| Dell | `root\dcim\sysman\wmisecurity`, `SecurityInterface.SetNewPassword("Admin", atual, nova)` | Exige o handle de segurança da senha atual (bytes) quando já há senha. Só no Dell Command \| Monitor ou nos modelos com o provedor WMI de fábrica. |
| Lenovo | `root\wmi`, `Lenovo_SetBiosPassword.SetBiosPassword("pap,atual,nova,ascii,us;")` | A string separada por vírgula proíbe vírgula e ponto e vírgula na senha (o alfabeto da 3.1 já evita). Em vários modelos, a **primeira** senha de supervisor não pode ser definida pelo WMI, só trocada: conferir no lab (o Lenovo do lab está sem senha). |

Todos devolvem um código; o agente traduz os conhecidos ("acesso negado", "senha atual
errada", "parâmetro inválido") e manda o número cru dos outros. Nenhum desses métodos devolve
a senha, e o agente nunca tenta lê-la.

**BitLocker**: trocar a senha de administrador não mexe nos PCRs que o TPM mede no padrão do
Windows (PCR 7 e 11); mudar outras opções da BIOS pode mexer. O job não muda nada além da
senha. Conferir no lab que a máquina sobe sem pedir a chave de recuperação.

## 5. O que este desenho não resolve

- **Senha de inicialização** (power-on) e **senha do disco** (HDD/NVMe): fora do escopo; o
  inventário mostra a de inicialização, e a frota não usa nenhuma das duas.
- **Máquina sem agente** ou com agente anterior à v0.18.2: sem caixa, sem job. O resumo
  mostra "não informada" em "Chave de cifragem".
- **Troca de placa-mãe**: a senha some com a placa; a guarda continua com a antiga. O
  inventário seguinte mostra `admin_password = false`, e a máquina entra na próxima rotação.
- **Chave da caixa perdida** (pasta de dados apagada, reinstalação limpa): o agente cria
  outra, e os envelopes antigos param de abrir. O servidor fecha de novo para a chave nova; a
  senha em si não se perde, porque a guarda é separada da caixa.

## 6. Decisões

Respondidas antes da Aula 21.4:

| | Decisão | Consequência |
|---|---|---|
| D1 | **(a) guarda no servidor** | "Revelar" no painel: só admin, motivo obrigatório, auditoria antes de mostrar e aviso por e-mail. Quem tomar o servidor lê as senhas (aceito; seção 7) |
| D2 | **uma senha por máquina** | sorteada pelo servidor |
| D3 | **12 caracteres em todos** | o alfabeto da 3.1 |
| D4 | **não se sabe** se há HP com Sure Admin | a Aula 21.5 lê esse modo no inventário antes de qualquer gravação, e a máquina nesse modo fica de fora |
| D5 | não oferecer remoção (recomendação mantida) | — |
| D6 | piloto | proposta da Aula 21.5: uma HP de bancada, depois um grupo pequeno de HP, depois a frota HP; Dell e Lenovo só depois de conferidas no laboratório |

O texto original das perguntas fica abaixo, para registro.

- **D1 — Onde fica a chave de guarda**: (a) no servidor, com "Revelar" no painel; (b) fora do
  servidor, com a revelação por comando no host. Recomendação: (b), salvo uso semanal.
- **D2 — Senha por máquina ou da frota**: por máquina (recomendação) ou uma senha da frota,
  digitada no painel e entregue fechada (corrige a leitura na máquina, não o alcance do vazamento).
- **D3 — Tamanho e alfabeto**: 12 caracteres, minúsculas e números sem ambíguos, ou mais
  longo onde o fabricante aceitar (perde a uniformidade entre modelos).
- **D4 — HP Sure Admin**: deixar as máquinas nesse modo de fora (o inventário marca) ou
  planejar o pacote assinado (outro desenho, com o certificado da organização).
- **D5 — Remover a senha pelo painel**: não oferecer (recomendação) ou oferecer, só admin,
  com motivo e aviso.
- **D6 — Piloto**: em quais máquinas a primeira rotação roda (uma de cada fabricante do
  lab, depois um grupo pequeno, depois a frota), e quem confere a entrada na BIOS com a
  senha revelada antes de seguir para o grupo seguinte.

## 7. A guarda (Aula 21.4)

O que já existe, antes de qualquer gravação na BIOS:

- **Chave de guarda.** Um par HPKE como o da caixa (`patchd-server bios keygen`), com outro
  propósito (`bios-guarda`). A privada fica no servidor, em `PATCHD_BIOS_ESCROW_KEY_FILE`
  (segredo do compose). Sem a variável, a guarda fica desligada e o painel diz isso.
- **Preparar.** Na aba Hardware da máquina, "Preparar senha nova" (só admin) sorteia 12
  caracteres e grava só o envelope (tabela `bios_passwords`, migration 0042), com a impressão
  da chave que fechou. Estado `pendente`: nada muda na máquina. No máximo uma pendente por
  máquina; pedir de novo devolve a mesma. Máquina aposentada não ganha senha nova, mas as
  guardadas dela continuam (a senha segue na placa).
- **O envelope leva o ID da máquina.** Trocar envelopes entre máquinas no banco faz a
  abertura falhar, em vez de mostrar a senha de outra máquina.
- **Revelar.** Só admin, com motivo (10 a 200 caracteres). A auditoria (`bios.senha.revelar`,
  com o motivo e sem a senha) é gravada **antes** de a senha aparecer; se ela não gravar, a
  senha não aparece. A página não fica em cache, a contagem de revelações aparece na linha, e
  a lista de notificações recebe um e-mail (sem a senha). O log registra quem, qual máquina e
  de onde, nunca a senha.
- **Fora da retenção.** A tabela não é limpa pela retenção: a senha vale enquanto a placa existir.

O risco aceito em D1: quem tomar o servidor (o processo, com o arquivo da chave) lê todas as
senhas guardadas. O banco sozinho, ou a cópia dele, não: só tem envelopes. Por isso a chave de
guarda **não** vai junto da cópia do banco, e tem duas cópias fora do servidor (perder a chave
é perder todas as senhas guardadas: a saída passa a ser a assistência do fabricante).

A gravação é a Aula 21.5 (seção 8).

## 8. A gravação (Aula 21.5)

**Só HP, por enquanto.** É onde o `.bat` rodou e onde o laboratório confere. Dell e Lenovo
entram na lista do job (`jobs.BIOSVendors`) depois de conferidas numa máquina de bancada.

**No painel** (aba Hardware, cartão Senha da BIOS, só admin), a senha pendente ganha o botão
**Gravar na BIOS**. Antes de criar o job, o servidor confere:

| Conferência | De onde | Se falhar |
|---|---|---|
| interface HP respondendo | inventário (Aula 21.1) | "por enquanto o patchd grava só na BIOS da HP" |
| HP Sure Admin desligado | inventário (`sure_admin`, Aula 21.5) | "esta BIOS só aceita mudanças por pacote assinado" |
| o inventário diz se há senha | inventário | "o inventário não diz se a BIOS tem senha" |
| chave da caixa no estado | estado (Aula 21.3) | "a máquina não informou a chave de cifragem" |
| nenhuma senha incerta | guarda | "confira na BIOS qual vale e resolva antes" |

**A senha atual.** BIOS sem senha: não há atual (é uma definição). BIOS com senha e uma ativa
na guarda: a atual sai da guarda. BIOS com senha e nada na guarda (a primeira troca, depois do
`.bat`): o admin digita a senha da frota no próprio formulário; ela vai fechada para a caixa e
não é gravada em lugar nenhum (nem no banco, nem na trilha, que registra só "senha atual
informada pelo admin").

**O job `bios_password`** leva a linha da guarda, a interface (`hp`), a impressão da chave da
caixa e os dois envelopes (propósitos `bios-senha` e `bios-senha-atual`). Assinado como todo
job; prazo de 24 h; não entra em campanha nem na linha de comando.

**No agente** (Windows), antes de tocar na BIOS: a chave da caixa é a mesma do pedido, os
envelopes abrem, a senha nova tem o formato da guarda, a interface HP responde e o Sure Admin
está desligado. Qualquer falha aqui volta como **rejeitado**, com "a BIOS não foi tocada", e a
senha volta a pendente. Passando, uma chamada: `HP_BIOSSettingInterface.SetBIOSSetting("Setup
Password", "<utf-16/>nova", "<utf-16/>atual")`. Depois, o inventário de depois vai antes do
resultado.

**A resposta decide a guarda:**

| O que aconteceu | Job | Guarda |
|---|---|---|
| código 0 e o inventário mostra a senha definida | concluído | **ativa** (a ativa anterior vira substituída) |
| código 0, mas o inventário não mostra a senha | falhou (incerto) | **incerta** |
| código 1, 4, 5 ou 6 (6 = a senha atual não confere) | falhou | **falhou**; a anterior continua |
| código 2, 3, desconhecido, ou o COM quebrou no meio | falhou (incerto) | **incerta** |
| recusado antes da chamada; expirou sem entrega | rejeitado / expirado | volta a **pendente** |
| entregue e sem resposta até expirar | expirado | **incerta** |

**Incerta trava.** Enquanto houver uma senha incerta na máquina, nada mais é gravado nela. Um
admin revela as duas (a nova e a anterior), confere qual a BIOS aceita na frente da máquina e
clica em "A nova vale" ou "A anterior vale" (auditado em `bios.senha.resolver`).

**Sem nova tentativa.** Senha atual errada repetida trava a BIOS da HP até reiniciar. O job
chama o fabricante uma vez; o código 6 aparece no cartão com o aviso, e a próxima tentativa é
uma decisão de alguém.

**O que ainda não está conferido** (o laboratório da 21.5 confere): o nome exato da
configuração do Sure Admin (`Enhanced BIOS Authentication Mode`) nos modelos da frota; que a
troca vale sem reiniciar; que a máquina com BitLocker sobe sem pedir a chave de recuperação.

## 9. A cópia de emergência fora do servidor (decisão R1 de `docs/bios-config.md`)

O pedido: se o servidor cair (ou o banco se perder), a equipe N1/N2 precisa entrar na BIOS.
A senha legível na máquina foi recusada (é o problema do `.bat`); a cópia é **o envelope da
guarda**, que só a chave privada de guarda abre. São três peças, e só quem junta a chave com
uma das cópias vê a senha:

| Peça | Onde fica | Quem lê |
|---|---|---|
| A chave privada de guarda | duas cópias offline: o cofre de senhas da N1/N2 e um envelope lacrado no cofre físico, longe do backup do banco | quem tem acesso ao cofre |
| `patchd-server bios export -out ARQUIVO` | qualquer backup (agendado; o arquivo é trocado inteiro, nunca fica pela metade) | ninguém, sem a chave |
| O Registro da máquina: `HKLM\SOFTWARE\patchd\BIOS`, valor `Copia` | a própria máquina | ninguém, sem a chave |

**O formato** (`internal/biospw/copy.go`) é uma linha:
`patchd-bios-1:<máquina>:<guarda>:<impressão da chave>:<envelope>`. O envelope é preso ao ID
da máquina: trocar o ID na linha não abre a senha de outra.

**O export** leva as senhas ativas, incertas e pendentes (uma pendente pode estar sendo
gravada agora), de máquinas ativas e aposentadas, com o nome de cada uma. Não precisa da chave
de guarda: dá para agendar no servidor sem mais um segredo por perto.

**O Registro.** O job `bios_password` leva a cópia da senha nova (campo `copy`), e o agente a
grava depois do código 0 (`Copia`, `CopiaEm`), apagando a incerta. Numa gravação incerta, a
nova vai para `CopiaIncerta` e a anterior fica em `Copia`: quem estiver na frente da máquina
tenta as duas. Falha ao gravar o Registro não desfaz nada (a BIOS já mudou): fica anotada no
resultado do job. O agente recusa, antes de tocar a BIOS, uma cópia de outra máquina. O
servidor só manda a cópia para agentes que avisam que a entendem (`bios_copy` no estado da
máquina); um agente anterior recusaria o campo novo. A máquina que já tinha senha antes desta
versão ganha a cópia na próxima troca pela guarda.

**A revelação offline**, num computador qualquer, sem servidor e sem banco:

```
patchd-server bios reveal -key guarda.key -copia "patchd-bios-1:..."           # valor lido no Registro
patchd-server bios reveal -key - -file bios-export.json -machine pc-lab-01        # chave colada do cofre
```

Ela **não entra na trilha** do patchd (não há banco). A regra: anotar no chamado e, com o
servidor de volta, trocar a senha pela guarda (Preparar senha nova), o que também renova as
cópias.

**O que isto não cobre:** quem tem a chave de guarda e uma cópia lê a senha sem deixar
rastro no patchd; por isso a chave fica em cofre com acesso registrado, não num notebook. E a
senha da frota que o `.bat` deixou legível na máquina continua lá até alguém apagar: depois da
primeira troca pela guarda, ela já não vale na BIOS, mas deve sair do Registro.
