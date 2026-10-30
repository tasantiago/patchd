# Configurações da BIOS pelo patchd

Continuação do `docs/bios-senha.md`. Ali o patchd aprendeu a gravar **só** a senha de
administrador; aqui ele lê todas as configurações da BIOS da HP e altera uma **lista fechada**
delas, com a senha que já está na guarda. Como no desenho da senha, nenhum valor real (senha,
nome de máquina, modelo exato do parque) entra neste arquivo.

## 1. O que entra

- **Leitura**: o agente do Windows lê as configurações da BIOS pela interface da HP e as manda
  no inventário. O painel mostra todas, na aba **BIOS** do equipamento, com o valor atual e os
  valores possíveis.
- **Gravação restrita**: só as configurações da lista fechada (seção 3) aparecem com um
  seletor. O admin escolhe os valores novos, informa o motivo, e o servidor cria o job
  `bios_settings`, assinado.
- Fora do escopo: Dell e Lenovo (entram quando forem conferidas no laboratório, como na
  senha), ordem de boot (lista ordenada, outro formato), e qualquer configuração que não seja
  de escolha entre valores (texto, número).

## 2. A leitura

| Classe (`root\HP\InstrumentedBIOS`) | O que vem | Painel |
|---|---|---|
| `HP_BIOSEnumeration` | nome, caminho, valor atual, valores possíveis, só leitura | lista; editável se estiver na lista fechada |
| `HP_BIOSString` | nome, caminho, valor, tamanho mínimo e máximo | só leitura |
| `HP_BIOSInteger` | nome, caminho, valor, limites | só leitura |
| `HP_BIOSOrderedList` | nome, caminho, valor (lista) | só leitura |
| `HP_BIOSPassword` | — | **não é lida aqui** (o inventário já diz só se a senha existe) |

Os valores vão no relatório de inventário (`Firmware.Settings`), que o servidor já guarda
inteiro: não há tabela nova. O relatório tem limites (quantidade de configurações, tamanho de
nome e valor) para uma BIOS estranha não inflar o banco. Uma configuração mudada gera um
inventário novo, e o histórico do equipamento mostra quando mudou.

## 3. A lista fechada

Os nomes mudam de modelo para modelo (a coleta de Wake on LAN já lida com isso). A lista é de
**regras por nome normalizado** (minúsculas, sem espaços, hífens, barras, parênteses), em
`internal/biosconfig`, usada pelo servidor (para mostrar o seletor) e pelo agente (para
recusar o que não estiver nela, mesmo com um job assinado).

**Exclusões, que vencem qualquer regra**: senha, TPM, limpar, restaurar padrões, Sure Admin
("Enhanced BIOS Authentication Mode"), presença física, atualização e proteção de firmware
(downgrade, Sure Start, flash), modo de boot e suporte legado, Absolute/Computrace (desligar é
permanente), e a placa de rede cabeada (desligar tira a máquina do patchd).

| Categoria | Regras (resumo) | Risco |
|---|---|---|
| Energia e Wake on LAN | Wake on LAN, economia profunda (S5 / Deep Sleep), depois de falta de energia, ligar ao conectar a fonte ou abrir a tampa | normal |
| Boot | Fast Boot, boot por USB, boot pela rede (PXE), atraso e alertas no boot, NumLock | normal |
| Virtualização e segurança | VT-x, VT-d, Secure Boot (só dentro do UEFI: valores que ligam o suporte legado são recusados) | **alto** |
| Portas e dispositivos | câmera, microfone, alto-falantes, leitor biométrico e de cartão, Bluetooth, Wi-Fi, portas USB | **alto** para Wi-Fi, Bluetooth e USB (podem tirar a máquina da rede ou desligar teclado e mouse) |

Uma configuração da lista só é editável quando é de escolha entre valores, não é só leitura
na BIOS, e o valor pedido está entre os possíveis que a própria máquina informou. Risco alto
pede a confirmação explícita no formulário.

## 4. A gravação (job `bios_settings`)

O servidor monta o job com as alterações (nome e valor) e a senha atual da guarda, fechada
para a caixa da máquina com um rótulo próprio (`bios-config`): um envelope deste job não serve
como senha atual de um `bios_password`, e vice-versa. O agente, nesta ordem:

1. confere a chave da caixa, abre a senha e confere a interface (HP) e o HP Sure Admin, como
   na senha. Qualquer falha é **recusa**: a BIOS não foi tocada;
2. relê as configurações e confere cada alteração contra a lista fechada, o "só leitura" e os
   valores possíveis **daquele momento**. Alteração cujo valor já é o pedido sai como "já
   estava assim";
3. **BitLocker** (decisão B1): com a proteção ligada na unidade do sistema, suspende por **um
   reinício** (`Win32_EncryptableVolume.DisableKeyProtectors(1)`) antes de gravar. Mudar o
   Secure Boot, a virtualização e outras opções altera o que o TPM mede na partida; sem a
   suspensão, a máquina pede a chave de recuperação. Se a suspensão falhar, recusa. Se nenhuma
   alteração for gravada, a proteção volta na hora;
4. grava **uma por vez**, uma chamada ao fabricante por configuração. Código 6 (senha não
   confere) **para tudo** na hora: a BIOS trava depois de algumas tentativas erradas. Código
   incerto (erro do firmware, tempo esgotado, quebra no meio) também para, e o resultado diz
   "incerto";
5. responde com uma linha por configuração (gravada, já estava assim, recusada pela BIOS,
   não tentada) e manda o inventário de depois.

As mudanças valem **no próximo reinício**, que o job não força: o painel avisa, e o reinício
segue pelo job `reboot` que já existe. Até lá, com o BitLocker suspenso, a chave do disco fica
em claro no volume: reiniciar cedo é o recomendado.

O servidor confere pelo inventário seguinte: o que ele mostra com o valor pedido sai como
conferido; o que ainda mostra o valor antigo fica anotado ("pode valer só depois do
reinício").

## 5. Quem pode o quê

| Ação | Quem | Registro |
|---|---|---|
| Ver as configurações | leitura e admin | — |
| Alterar (uma máquina) | admin, com motivo | `bios.config.job` com as alterações (nome e valor, nunca senha) |

## 6. Decisões

| | Decisão | Consequência |
|---|---|---|
| C1 | leitura de tudo e gravação restrita | a lista fechada da seção 3 |
| C2 | categorias: energia/WoL, boot, virtualização/segurança, portas/dispositivos | TPM, modo de boot e placa cabeada ficam fora mesmo assim (seção 3) |
| B1 | BitLocker suspenso por um reinício | volta sozinho depois do reinício; sem gravação, volta na hora |
| S1 | a senha atual vem da guarda | máquina sem senha ativa na guarda: o painel pede para gravar uma pela guarda antes |
| R1 | cópia **cifrada** da guarda no Registro da máquina e no `bios export`, aberta só com a chave de guarda offline; a senha legível na máquina, não | ver seção 7 e `docs/bios-senha.md`, seção 9 |

## 7. A senha também no Registro da máquina (R1, decidida)

O pedido: além da guarda, deixar a senha registrada na própria máquina, para a equipe N1/N2
(as únicas com administrador local) entrar na BIOS quando o servidor não responder.

A senha **legível** no Registro foi recusada: é o problema número 1 do `.bat`
(`docs/bios-senha.md`, seção 1). Qualquer conta com administrador local, ou qualquer programa
rodando como administrador ou SYSTEM (malware incluído), leria a senha e trocaria qualquer
configuração da BIOS pelo mesmo WMI que o patchd usa.

**Decisão (30/10/2026):** a máquina guarda no Registro a **cópia cifrada** da guarda, e o
servidor exporta as mesmas cópias para fora dele; só a chave privada de guarda, guardada
offline, abre qualquer uma delas. Desenho em `docs/bios-senha.md`, seção 9.
