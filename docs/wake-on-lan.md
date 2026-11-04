# Ligar pela rede: acordar, atualizar, desligar (Módulo 22)

O objetivo do módulo: ligar as máquinas de madrugada, dentro da janela do grupo de implantação, deixar as
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
motivos em texto fixo: o cartão da máquina e o resumo do parque contam pelos mesmos textos.

## As aulas

- **22.1 (feita): prontidão, só leitura.** O inventário ganha o bloco `wake`; a aba Hardware
  mostra o cartão "Ligar pela rede"; `patchd-server inventory summary -dir PASTA` resume uma
  pasta de inventários avulsos (coletados pelo PsExec, sem o servidor), com o firmware da 21.2
  junto.
- **22.2 (feita): acordar.** Um job assinado manda um agente ligado da mesma sub-rede repetir o
  pacote mágico para uma lista fechada de MACs (só broadcast local, porta 9, sem destino livre:
  o agente não vira ferramenta de varredura). Botão "Ligar pela rede" na aba Hardware. Detalhes
  na seção "Acordar" abaixo.
- **22.3: a janela.** No início da janela do grupo de implantação, o servidor acorda as máquinas desligadas que
  têm pendências; no fim, um job de desligar só para as que o patchd acordou e sem usuário
  conectado. Cada máquina guarda "acordou / não acordou": é a medida do módulo.
- **22.4: a BIOS.** Ligar o Wake on LAN e desligar a economia profunda pela interface do
  fabricante, com a senha da guarda (Aulas 21.4 e 21.5). Escrita no firmware: mesmas regras da
  21.5 (uma chamada, sem nova tentativa, o resultado decide).

## Acordar (Aula 22.2)

**O pacote.** 6 bytes `FF` e 16 cópias do MAC (102 bytes), em UDP para o broadcast da sub-rede,
porta 9, três vezes com 100 ms entre elas (`internal/wol`). O broadcast é calculado pelo
agente a partir das redes das próprias interfaces; /31, /32 e fora do IPv4 não têm broadcast.

**Quem manda.** O servidor não está na rede das estações (e o pacote não passa pelo
roteador): manda um job `wake_on_lan` a até **3 repetidores**, máquinas da **mesma sub-rede**
(mesmo IPv4 e mesmo prefixo no último estado) com check-in nas **últimas 2 horas** e agente
que se declara repetidor (`wake_relay` no estado; agente antigo não é escolhido). Os de
check-in mais recente primeiro. Mais de um encurta a espera: o job sai no próximo check-in de
cada um, que é de hora em hora.

**O job.** Assinado como qualquer job (vale 2 horas), com a máquina a acordar, os MACs (de 1 a
8, na forma normal, nem zero nem de grupo) e a rede (`192.0.2.0/24`, de /8 a /30). O agente
confere tudo de novo e só manda pelas interfaces que estão nessa rede; se ele não está mais
nela, **recusa** e nada sai pela rede. Não há porta, endereço nem conteúdo vindos do servidor.

**Os MACs.** As placas com cabo do bloco `wake` do inventário (Aula 22.1), menos as com Wake on
Magic Packet desligado ou sem suporte; sem o bloco (Linux, macOS, agente antigo), as placas
com MAC de fabricante e IPv4 do último estado.

**O resultado.** O job do repetidor fica "concluído" quando ele conta que mandou (o efeito na
rede só ele vê). O pedido (`wake_requests`, migração 0055) guarda quem pediu, a rede, os
MACs, os repetidores, quem mandou primeiro e quando, e **a resposta**: o primeiro check-in da
máquina até 6 horas depois do pacote. O cartão "Ligar agora" da aba Hardware mostra os 5
pedidos mais novos ("respondeu 3 min depois do pacote" ou "não respondeu"). Um pedido em
andamento segura o botão; com o pacote mandado e sem resposta, dá para pedir de novo depois
de 10 minutos. Cada pedido entra na auditoria (`maquina.ligar`).

**Sem repetidor.** Nenhuma máquina ligada na sub-rede: o painel diz qual sub-rede e não cria
nada. A saída é ter uma máquina sempre ligada por sub-rede (um servidor, uma impressora não
serve: precisa do agente).

## Segurança

O pacote mágico não tem autenticação: qualquer um na rede local liga uma máquina com o MAC
dela. Isso já é verdade hoje para qualquer máquina com WoL ligado; o patchd não piora. O que
o patchd acrescenta é quem pede (job assinado, auditado) e quem repete (agente da mesma
sub-rede, lista fechada de MACs, sem destino escolhido pelo servidor).
