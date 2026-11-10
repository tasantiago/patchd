# Terminal do administrador

Pedido de 10/11/2026: um terminal para o perfil administrador rodar comandos nas máquinas,
totalmente auditável. Fica na aba **Terminal** da página de cada máquina.

Nenhum valor real (nome de máquina, IP, usuário) entra aqui.

## 1. O que é (e o que não é)

O painel não tem JavaScript (a CSP não deixa nenhum script rodar), então não há terminal
interativo: nada de cursor, `vim`, `top` ou programa que espere o teclado. É um **console de
comandos**: uma linha por vez, que roda na máquina e devolve a saída em segundos, pelo canal
em tempo real. A aba mostra a transcrição (o comando, quem, quando, o código de saída e a
saída), do mais antigo para o mais novo, como num terminal.

Um admin já roda qualquer coisa como SYSTEM pelas máquinas em Automação › Scripts; o terminal
não abre uma porta nova, só encurta o caminho. Por isso o peso está no registro e nas travas.

## 2. Decisões

| | Decisão | Consequência |
|---|---|---|
| T1 | **Desligado por padrão** | Liga em Configuração › Parâmetros › Agentes › Terminal do administrador (`PATCHD_TERMINAL`). A mudança fica na auditoria dos parâmetros |
| T2 | **Motivo obrigatório por sessão** | De 10 a 200 caracteres; a sessão vale 15 minutos sem comando (cada comando renova) e é de quem abriu, naquela máquina |
| T3 | **Registro por 365 dias** | Comando, saída e sessão saem sozinhos depois disso; pelo painel nada se edita nem se apaga |
| T4 | **PowerShell e cmd no Windows, bash no Linux e no macOS** | Seletor na linha de comando; o que a máquina não tem é recusado pelo agente |
| T5 | **Uma máquina por vez** | Para várias, Automação › Scripts (com conferência) |

## 3. O caminho de um comando

1. O admin abre a aba, diz o **motivo** e abre a sessão. A auditoria (`terminal.abrir`, com o
   motivo) é gravada **antes**; se ela não gravar, a sessão não abre. A lista de notificações
   recebe um e-mail (quem, qual máquina, o motivo; sem comandos).
2. Ele digita o comando, escolhe o interpretador e o tempo máximo (30 s a 10 min; padrão 60 s).
   O servidor confere (uma linha, até 4.000 bytes, sem caracteres de controle) e grava a
   auditoria (`terminal.comando`, com o texto) **antes** de qualquer coisa sair.
3. O comando vira um job assinado `comando` (a mesma chave dos outros jobs), que vale **10
   minutos**: a máquina desligada não roda um comando velho horas depois. O registro do
   terminal (`terminal_commands`) guarda o texto, quem, o IP e o job; se o registro não
   gravar, o job é cancelado.
4. O agente recebe pelo canal em tempo real, confere a assinatura e roda:
   - PowerShell: `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass
     -EncodedCommand ...` (o texto vai em UTF-16, sem regra de aspas no caminho), com a saída
     em UTF-8;
   - cmd: `cmd.exe /d /s /c "chcp 65001>nul & COMANDO"`;
   - bash: `bash -c COMANDO` (ou `sh`, onde não há bash).

   Com a conta do agente (SYSTEM ou root), na raiz do disco do sistema, **sem entrada pelo
   teclado** (o programa que lê recebe fim de arquivo), com a árvore de processos encerrada no
   tempo máximo (Job Object no Windows, grupo de processos no Unix). O texto do comando vai
   também para o log do agente: quem administra a máquina vê o que rodou nela.
5. O resultado volta na hora: o código de saída e o **fim da saída, até 64 KiB** (o que
   passar é cortado no começo, com o aviso). O servidor grava no registro uma vez (um segundo
   resultado para o mesmo job não troca o primeiro) e a aba, que se recarrega a cada 3
   segundos enquanto espera (por até 90 segundos), mostra.

Cada comando é independente: não há pasta atual nem variável de um para o outro. Para mudar
de pasta, `cd PASTA; comando` na mesma linha.

## 4. O que fica registrado

| Onde | O quê | Prazo |
|---|---|---|
| `terminal_sessions` | quem abriu, a máquina, o motivo, o IP, quando abriu e até quando vale | 365 dias |
| `terminal_commands` | o texto exato, o interpretador, o tempo máximo, quem, o IP, o job, o código de saída, a saída, se foi cortada e o que o agente disse | 365 dias |
| Auditoria (`terminal.abrir`, `terminal.comando`, `terminal.fechar`) | o mesmo, na trilha geral, com o `request_id` do log | a retenção da auditoria |
| Jobs | o job `comando`, com o estado | a retenção dos jobs |
| Log do servidor e do agente | a sessão e cada comando, com o texto | os logs |

**Consultar:** Configuração › Auditoria › **Comandos do terminal** (`/painel/terminal`, só
admin): todos os comandos, do mais novo para o mais antigo, com o filtro por máquina e por
administrador, e a saída de cada um.

**Não digite senha na linha de comando:** o texto fica guardado em todos esses lugares.

## 5. Travas

- Só o perfil **administrador** vê a aba, envia comandos e lê o histórico.
- Só agentes que anunciam `terminal-v1` (desta versão em diante). Instalado com
  **`-no-terminal`**, o agente não anuncia e recusa o job; a opção fica na linha de comando
  do serviço, que só um administrador da própria máquina muda: o servidor não alcança. Use em
  servidores e máquinas sensíveis.
- O comando sem sessão válida não sai (a sessão vencida pede o motivo de novo).
- A resposta vem escapada no HTML (a saída de um comando não vira marcação na página).

## 6. O que isto não resolve

- Quem tomar o servidor (a chave de jobs fica nele) já roda o que quiser nas máquinas pelos
  outros tipos de job; o terminal não muda esse limite. O `-no-terminal` protege a máquina
  deste caminho, não dos outros.
- Um admin mal-intencionado tem o registro contra ele, não um bloqueio: o terminal não tem
  lista de comandos proibidos (ela seria contornável e daria falsa segurança).
- A saída além dos 64 KiB não é guardada: para relatórios grandes, grave num arquivo na
  máquina e leia o fim, ou use Scripts.
