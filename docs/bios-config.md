# Configurações da BIOS pelo patchd

Continuação do `docs/bios-senha.md`. Ali o patchd aprendeu a gravar **só** a senha de
administrador; aqui ele lê todas as configurações da BIOS da HP e altera uma **lista fechada**
delas, com a senha que já está na guarda. Como no desenho da senha, nenhum valor real (senha,
nome de máquina, modelo exato do parque) entra neste arquivo.

## 1. O que entra

- **Leitura**: o agente do Windows lê as configurações da BIOS pela interface do fabricante
  (HP; Dell e Lenovo na seção 8) e as manda
  no inventário. O painel mostra todas, na aba **BIOS** do equipamento, com o valor atual e os
  valores possíveis.
- **Gravação restrita**: só as configurações da lista fechada (seção 3) aparecem com um
  seletor. O admin escolhe os valores novos, informa o motivo, e o servidor cria o job
  `bios_settings`, assinado.
- Fora do escopo: ordem de boot (lista ordenada, outro formato) e qualquer configuração que
  não seja de escolha entre valores (texto, número).

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

## 8. Dell e Lenovo

Pedido de 30/10/2026: o mesmo painel, a mesma lista fechada e os mesmos jobs (`bios_settings`
e `bios_password`) para Dell e Lenovo. O que muda é só a conversa com o firmware
(`internal/biosvendor` para ler, `internal/bioswrite` para gravar):

| | Dell | Lenovo |
|---|---|---|
| Ler | `root\dcim\sysman\biosattributes`: `EnumerationAttribute`, `IntegerAttribute`, `StringAttribute` (o valor atual vem como lista de um item) | `root\WMI`: `Lenovo_BiosSetting.CurrentSetting` ("Nome,Valor;[Optional:...]"); sem o `[Optional:...]`, `Lenovo_GetBiosSelections`, só para a lista fechada |
| Alterar | `BIOSAttributeInterface.SetAttribute`, um por configuração | `Lenovo_SetBiosSetting.SetBiosSetting` por configuração e, no fim, `Lenovo_SaveBiosSettings`: **sem o salvar, nada vale** |
| Senha | `SecurityInterface.SetNewPassword("Admin", atual, nova)`; com senha, a atual vai também como "handle" (bytes) | `Lenovo_SetBiosPassword.SetBiosPassword("pap,atual,nova,ascii,us;")` |
| Respostas | números: 0 ok, 3 senha não confere (para tudo), 2 e 4 recusa, 1, 5, 6 incerto | texto: `Success`, `Access Denied` (para tudo), `Not Supported`, `Invalid Parameter`, `System Busy`; outro, incerto |
| Sure Admin | não há equivalente lido; a Dell sem o provedor WMI de fábrica (só Dell Command \| Monitor) não tem as classes e fica sem configurações | não lido; a Lenovo com autenticação por certificado recusa as chamadas, e o job falha sem gravar |

**As respostas viram uma só** (`bioswrite.Reply`): ok, senha não confere (nenhuma outra
chamada no mesmo job), recusa clara, ou incerta. O resto do job não muda: BitLocker suspenso
por um reinício, uma chamada por configuração, nada de senha em log ou resultado.

**Salvar (Lenovo).** Depois das gravações, o agente chama o salvar. Se ele recusa, as gravadas
viram "recusada pela BIOS" (nada foi salvo) e o BitLocker volta; se quebra no meio, viram
incertas e o BitLocker fica suspenso até o reinício.

**Vírgula e ponto e vírgula** separam os campos da Lenovo: nome, valor ou senha com eles são
recusados pelo agente antes da chamada. As senhas da guarda não têm; a senha padrão do parque digitada
na primeira troca pode ter, e aí a troca precisa ser feita na BIOS.

**A lista fechada** ganhou os nomes da Dell (`WakeOnLan`, `AcPwrRcvry`, `UefiNwStack`,
`Virtualization`, `VtForDirectIo`, `WirelessLan`, `UsbPortsExternal`...) e da Lenovo
(`WakeOnLAN`, `QuickBoot`, `USBBIOSSupport`, `VirtualizationTechnology`, `WirelessLANAccess`,
`USBPortAccess`...). Ficam de fora, além das exclusões da seção 3: a placa cabeada
(`EthernetLANAccess`, `EmbNic1`), o TPM da Lenovo (`SecurityChip`) e o modo do Secure Boot da
Dell (`SecureBootMode`: o modo Audit desliga a conferência).

**O que não está conferido** (sem Dell nem Lenovo no laboratório desta versão): os nomes dos
parâmetros dos métodos da Dell em cada geração; se a Lenovo aceita a **primeira** senha de
supervisor pelo WMI (em vários modelos não: o job volta "acesso negado" e a primeira senha é
definida na BIOS); e os nomes das configurações nos modelos do parque. O piloto de cada
fabricante começa por uma máquina de bancada, como foi com a HP.

## 9. O padrão da BIOS (qualquer marca e modelo)

O pedido (10/11/2026): padronizar a BIOS do parque inteiro, independente de modelo ou marca:
**senha de administrador**, **Wake on LAN ligado** e **boot por USB desligado**. Fica em
**Configuração › BIOS › Padrão da BIOS** (`/painel/bios/padrao`, só admin).

**Objetivos, não nomes.** Cada fabricante (e às vezes cada geração) chama a mesma coisa de um
jeito, com valores diferentes. O padrão é escrito como objetivo, e `internal/biosstd` o resolve
para cada máquina com as configurações que **a própria BIOS informou** no inventário (seção 2).
O patchd só propõe mudar o que a máquina mostrou que tem, para um valor que ela mostrou que
aceita, e toda mudança passa de novo pela lista fechada (seção 3), no servidor e no agente.

| Objetivo | O que conta | Exemplos (de → para) |
|---|---|---|
| Senha de administrador | a senha da guarda valendo na BIOS | é a senha em lote (`docs/bios-senha.md`, seção 12) |
| Wake on LAN ligado | o WoL da placa cabeada (não o do Wi-Fi, o da dock nem a política de senha do WoL) e a economia profunda do desligado, que corta a energia da placa | HP `Wake On LAN`: Disable → Boot to Hard Drive; HP `S5 Maximum Power Savings`: Enable → Disable; Dell `WakeOnLan`: Disabled → LanOnly (WlanOnly → LanWlan); Dell `DeepSleepCtrl` → Disabled; Lenovo `WakeOnLAN`: Disable → ACOnly (notebook) ou Primary (desktop) |
| Boot por USB desligado | a configuração que liga o boot por armazenamento USB | HP `USB Storage Boot`: Enable → Disable; Dell `UsbEmu` ("Enable USB Boot Support"): Enabled → Disabled; outros nomes com "USB Boot" / "Boot from USB" |

Para ligar o WoL, o valor preferido mantém o boot normal ("Boot to Hard Drive", "LanOnly",
"ACOnly"...); "Boot to Network" (a máquina acorda e tenta o PXE) só entra se for o único.
Um WoL já ligado, com qualquer valor, conta como conforme.

Ficam de fora de propósito: a `UsbEmuNoUsbBoot` das Dell antigas (o sentido é o contrário), a
ordem de boot (é lista, não escolha), o suporte legado a USB da Lenovo (`USBBIOSSupport`: é o
teclado USB na tela da BIOS, não o boot) e desligar as portas USB (tiraria teclado e mouse).

**A conformidade.** Para cada objetivo, a tela conta a frota em cinco colunas, e cada número
abre a lista das máquinas com o valor atual na BIOS e o motivo:

| Coluna | Quer dizer |
|---|---|
| conforme | a BIOS já está como o padrão pede |
| a corrigir | há o que mudar, e a máquina pode receber o job agora |
| aguardando reinício | o job foi concluído e o inventário ainda mostra o valor antigo (vários modelos só mudam depois do reinício); por até 7 dias, sem novo pedido |
| o modelo não tem a configuração | a BIOS não informa nada deste objetivo: nenhuma tentativa às cegas |
| não dá agora | o motivo vai junto: senha fora da guarda, senha incerta, alteração a caminho, falha nas últimas 24 h, configuração só leitura, inventário sem as configurações, fora do alcance |

**A senha vem primeiro.** Gravar uma configuração numa BIOS com senha exige a senha em uso, e
o patchd só a tem quando ela está na guarda. A máquina com senha fora da guarda (a do `.bat`,
uma padrão) fica em "não dá agora" até passar pela senha em lote; a sem senha recebe as
configurações já, e a senha depois (ou antes: a ordem é livre).

**O lote.** Marque os objetivos, o grupo de implantação e o tamanho (1 a 500 máquinas) e peça a
conferência. Ela mostra **o que o padrão vira em cada fabricante** (configuração, de, para, e
quantas máquinas) e a lista das máquinas com as alterações. Nada é enviado nesse passo. Com o
motivo, o servidor refaz a lista, compara com a conferida e cria **um job `bios_settings` por
máquina**, com as mesmas regras de sempre (seção 4): BitLocker suspenso por um reinício, uma
chamada por configuração, parar na primeira senha recusada, a senha da guarda fechada para a
caixa da máquina. A trilha `bios.padrao.lote` é gravada antes de qualquer job; cada máquina
ganha o `bios.config.job` de sempre, com a impressão do lote. Uma alteração de risco alto
(seção 3) nunca sai pelo padrão; os três objetivos não têm nenhuma.

**A ordem recomendada:** 1 máquina de cada fabricante e modelo, conferida na frente dela
depois do reinício (o WoL acorda? o pendrive deixa de aparecer no menu de boot?); depois o
resto por grupo de implantação.

**O que não está conferido:** os nomes e os valores nos modelos do parque. Um modelo com outro
nome para o boot por USB aparece em "o modelo não tem a configuração"; a lista da coluna mostra
quais são, e o nome entra em `internal/biosstd` (e na lista fechada) com o piloto daquele modelo.

## 10. Em linguagem simples

O pedido (12/11/2026): mostrar as configurações da BIOS com menos termo técnico, iguais para
qualquer marca e modelo.

**O dicionário** (`internal/biosplain`) traduz o nome e o valor de cada configuração para
português simples. O mesmo assunto ganha o mesmo nome em qualquer fabricante: o `Wake On LAN`
da HP, o `WakeOnLan` da Dell e o `WakeOnLAN` da Lenovo viram "Ligar pela rede"; `Boot to Hard
Drive`, `LanOnly` e `ACOnly` viram "Ligado, iniciando o Windows", "Ligado, só pelo cabo" e
"Ligado, só na tomada". Cada configuração traz uma frase do que ela faz na prática.

A tradução é só para mostrar. O job continua levando o nome e o valor exatos da BIOS, e a tela
mostra o original em letra pequena ("na BIOS: WakeOnLan", "Disabled") para quem precisa
conferir no computador. O que não está no dicionário aparece com o nome do fabricante. Duas
opções com a mesma tradução levam o valor original junto, para não ficarem iguais no seletor.

**A aba BIOS do equipamento** ficou assim:

- **Resumo da BIOS**, com os mesmos itens para qualquer modelo: senha de administrador, ligar
  pela rede, iniciar por pendrive, inicialização segura (Secure Boot), modo de inicialização,
  chip de segurança (TPM), virtualização e o que o computador faz quando a energia volta. Cada
  item diz como está nesta máquina e, nos três do padrão (seção 9), se está conforme. O modelo
  que não tem a configuração aparece como tal.
- **Configurações da BIOS**, agrupadas por assunto (Ligar e desligar, Inicialização, Segurança,
  Portas e dispositivos), com "Como está" e "Mudar para" em português. As de risco alto ganham
  "cuidado" e o motivo em uma linha.
- As outras configurações (só consulta) vêm traduzidas primeiro, e as que o dicionário não
  conhece depois, com o nome do fabricante.

O Padrão da BIOS (seção 9), a lista de alterações pedidas e o cartão de ligar pela rede da aba
Hardware usam o mesmo dicionário. A trilha de auditoria continua com os nomes e valores
originais: é o registro técnico do que foi gravado.

**Para acrescentar uma configuração** que apareça com o nome do fabricante: uma entrada em
`internal/biosplain` (título, frase, assunto e os pedaços do nome normalizado), e o teste do
pacote confere que o mesmo assunto tem o mesmo nome em cada marca.
