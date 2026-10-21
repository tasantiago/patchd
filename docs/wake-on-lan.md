# Ligar pela rede: acordar, atualizar, desligar (Módulo 22)

O objetivo do módulo: ligar as máquinas de madrugada, dentro da janela do anel, deixar as
atualizações rodarem e desligar de novo só as que o patchd acordou. Este documento é o mapa;
cada aula preenche uma parte.

Nenhum valor real (nome de máquina, MAC, sub-rede) entra aqui.

## O que impede uma máquina de acordar

| Onde | O quê | Quem lê (Aula 22.1) |
|---|---|---|
| BIOS | Wake on LAN desligado; economia profunda (Deep Sleep da Dell, S5 Maximum Power Savings da HP) | a interface do fabricante (a mesma da Aula 21.1) |
| Placa de rede | "Wake on Magic Packet" desligado no driver; "Permitir que este dispositivo ative o computador" desmarcado | `MSFT_NetAdapterPowerManagementSettingData`, `MSPower_DeviceWakeEnable` |
| Rede física | sem cabo (Wi-Fi quase nunca acorda); notebook fora da tomada | `MSFT_NetAdapter` (meio físico), chassi |
| Windows | Fast Startup muda o "desligar" para uma hibernação híbrida: o efeito depende do driver | `HiberbootEnabled` |
| BitLocker | PIN na partida: a máquina acorda e para na tela do PIN | `Win32_EncryptableVolume.GetKeyProtectors` |
| Rede lógica | o pacote mágico é broadcast e não passa pelo roteador | (Aula 22.2: um agente da mesma sub-rede repete o pacote) |

A decisão (pronta, bloqueada, sem informação) fica no servidor (`internal/wake`), com os
motivos em texto fixo: o cartão da máquina e o resumo da frota contam pelos mesmos textos.

## As aulas

- **22.1 (feita): prontidão, só leitura.** O inventário ganha o bloco `wake`; a aba Hardware
  mostra o cartão "Ligar pela rede"; `patchd-server inventory summary -dir PASTA` resume uma
  pasta de inventários avulsos (coletados pelo PsExec, sem o servidor), com o firmware da 21.2
  junto.
- **22.2: acordar.** Um job assinado manda um agente ligado da mesma sub-rede repetir o pacote
  mágico para uma lista fechada de MACs (só broadcast local, porta 9, sem destino livre: o
  agente não vira ferramenta de varredura). Botão "Ligar" no detalhe da máquina.
- **22.3: a janela.** No início da janela do anel, o servidor acorda as máquinas desligadas que
  têm pendências; no fim, um job de desligar só para as que o patchd acordou e sem usuário
  conectado. Cada máquina guarda "acordou / não acordou": é a medida do módulo.
- **22.4: a BIOS.** Ligar o Wake on LAN e desligar a economia profunda pela interface do
  fabricante, com a senha da guarda (Aulas 21.4 e 21.5). Escrita no firmware: mesmas regras da
  21.5 (uma chamada, sem nova tentativa, o resultado decide).

## Segurança

O pacote mágico não tem autenticação: qualquer um na rede local liga uma máquina com o MAC
dela. Isso já é verdade hoje para qualquer máquina com WoL ligado; o patchd não piora. O que
o patchd acrescenta é quem pede (job assinado, auditado) e quem repete (agente da mesma
sub-rede, lista fechada de MACs, sem destino escolhido pelo servidor).
