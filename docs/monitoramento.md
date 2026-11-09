# Monitoramento de desempenho e alertas

O primeiro passo do patchd para além de patch e inventário (pedido de 30/10/2026; até aqui,
"monitoramento de desempenho" estava em **Fora de escopo (v1)** em `docs/requisitos.md`). O
objetivo é saber, sem abrir a máquina, quando CPU, memória ou disco de uma estação passaram do
limite, e ver a curva de cada uma.

## 1. Decisões

| | Decisão | Consequência |
|---|---|---|
| M1 | **Check-in antecipado por mudança**, senão a cada 1 hora | O agente mede a cada minuto e manda tudo no check-in; adianta o check-in quando algo relevante muda (seção 3). Sem tráfego novo nas horas calmas; um alerta chega em minutos |
| M2 | **CPU, memória e disco** nesta versão | Disponibilidade, serviços e saúde do disco ficam para depois |
| M3 | **Só no painel** | Tela Alertas, contador no menu e a aba Desempenho do equipamento. Nada de e-mail, Teams ou webhook por enquanto |
| M4 | **7 dias por minuto + 90 dias por hora** | Pontos de minuto por 7 dias (curva detalhada) e média e pico por hora por 90 dias (tendência) |

## 2. O que o agente mede

A cada minuto, no Windows, no Linux e no macOS:

| Medida | Windows | Linux | macOS |
|---|---|---|---|
| CPU (%, média do minuto) | `GetSystemTimes` | `/proc/stat` | `ps -A -o %cpu` somado / núcleos |
| Memória em uso (%) | `GlobalMemoryStatusEx` | `/proc/meminfo` (`MemAvailable`) | `vm_stat` + `hw.memsize` |
| Disco livre (% por volume fixo) | a mesma coleta de discos do estado da máquina (Aula 12.1) | idem | idem |

As amostras ficam na memória do agente (no máximo 24 horas; reiniciar o agente perde o que
ainda não foi enviado) e vão no próximo check-in, que as tira da fila só quando o servidor
aceita.

## 3. O check-in antecipado

O agente já tinha o check-in antecipado do autoatendimento (Aula 17.3). Agora uma sentinela
também o adianta quando:

| Mudança | Como o agente percebe | Conferida a cada |
|---|---|---|
| Usuário entrou ou saiu | a lista de usuários conectados do estado da máquina | 1 min |
| IP mudou | os endereços das interfaces | 1 min |
| Programa instalado, removido ou atualizado | uma impressão da lista de software (só nome, versão e editor) | 15 min (Windows e Linux; no macOS a lista é cara e fica no check-in de hora em hora) |
| Desempenho passou do limite, ou voltou | os limites que o servidor manda no check-in (seção 4), por N minutos seguidos | 1 min |

A sentinela só **antecipa** o check-in: quem decide o alerta é o servidor (P-01). Para uma
mudança em massa (todas as máquinas recebendo um programa juntas) não virar uma avalanche, o
check-in antecipado espera um sorteio de até 2 minutos e não vem mais de uma vez a cada 5
minutos por máquina.

## 4. Os limites e os alertas

Os limites ficam em **Configuração › Parâmetros › Monitoramento**, para o parque todo:

| Parâmetro | Padrão |
|---|---|
| `PATCHD_MONITOR_CPU_PCT` | CPU acima de 90% |
| `PATCHD_MONITOR_MEM_PCT` | memória acima de 90% |
| `PATCHD_MONITOR_DISK_FREE_PCT` | disco com menos de 10% livre |
| `PATCHD_MONITOR_MINUTES` | por 10 minutos seguidos |

O servidor manda os limites no check-in, e o agente os usa só para decidir quando adiantar o
próximo. O servidor confere cada lote: N amostras seguidas acima do limite **abrem** um alerta
(máquina, medida e, no disco, o volume); N seguidas abaixo o **fecham**. Um alerta aberto
guarda o pico e o último valor. O admin pode **reconhecer** um alerta (com nota), o que o tira
do contador sem fechá-lo; ele só fecha quando a medida volta.

**Em lote** (tela Alertas, só admin): marque os alertas na tabela, ou escolha "todos os
abertos" (do filtro de medida em uso), e clique **Fechar** ou **Reconhecer**. Nada muda antes
da conferência, que mostra quantos e quais (até 50, e a contagem do resto); se a lista mudar
entre a conferência e a confirmação (um alerta fechou sozinho, abriu ou foi reconhecido), a
tela volta para conferir de novo. Cada lote vai para a auditoria (`alerta.fechar-lote` ou
`alerta.reconhecer-lote`), com a nota e os IDs.

Fechar à mão serve para o que não vai voltar sozinho: a máquina desligada ou aposentada com
alerta aberto, o volume que saiu (um disco removido), a manutenção já resolvida. O alerta fica
"fechado à mão", com quem fechou e a nota, e a contagem da medida recomeça: **se o problema
continuar, um alerta novo abre** depois dos minutos seguidos. Fechar não esconde problema que
continua.

### Limpar temporários (alertas de disco)

Num alerta de disco aberto, o botão **Limpar temporários** da linha (ou o mesmo botão do lote,
que pega só os alertas de disco entre os marcados ou os do filtro) abre a conferência: as
máquinas, uma vez cada, com os volumes em alerta, e os itens da limpeza, de uma lista fechada:

| Item | Windows | Linux | Padrão |
|---|---|---|---|
| temporários do sistema | `%SystemRoot%\Temp` | `systemd-tmpfiles --clean` | marcado |
| temporários dos usuários | `AppData\Local\Temp` de cada perfil | não se aplica | marcado |
| restos do patchd | `pacotes`, `tmp` e `bundles` da pasta de dados do agente | idem | marcado |
| cache de download de atualizações | `SoftwareDistribution\Download` (o serviço Windows Update é parado e religado se estava rodando) | `apt-get clean` ou `dnf clean packages` | desmarcado |
| limpeza de componentes | `DISM /Online /Cleanup-Image /StartComponentCleanup` (até 2 horas; usa disco e CPU) | `journalctl --vacuum-size=200M` | desmarcado |

Confirmada (com a mesma lista da conferência), cada máquina recebe um job assinado `limpeza`
(validade de 2 horas), e a auditoria guarda `limpeza.criar` com os itens e os IDs dos jobs. O
resultado do job diz o espaço livre no disco do sistema antes e depois e quanto cada item
liberou; com o disco de volta abaixo do limite, o alerta fecha sozinho pela regra de sempre.

O agente roda como SYSTEM (root) e as pastas de temporários dos usuários são graváveis por
eles, então a varredura é defensiva:

- só sai **arquivo comum sem mudança há mais de 48 horas**; arquivo em uso fica e conta como
  pulado; as pastas velhas que esvaziaram saem depois, da mais funda para a mais rasa;
- **não segue link**: link simbólico, junção ou qualquer ponto de nova análise não é aberto
  nem apagado (no Windows o arquivo é aberto pelo próprio handle, sem seguir o ponto de nova
  análise, o caminho final é conferido dentro da pasta e a exclusão é pelo handle, o que fecha
  a troca da pasta por uma junção entre a conferência e a exclusão);
- a pasta raiz também é conferida: se `C:\Windows\Temp` ou o `Temp` de um perfil for uma
  junção, o item é pulado; perfis que são junção também;
- nada de Downloads, Documentos, Lixeira ou caminhos vindos do painel: o job leva só os nomes
  dos itens, e o agente conhece as pastas.

Agente anterior: rejeita o job (tipo desconhecido) e o job aparece como falho com o motivo.

## 5. Guardar e mostrar

- **Banco:** `perf_samples` (máquina, minuto, CPU, memória, disco livre por volume) por 7 dias;
  `perf_hourly` (média e pico por hora) por 90 dias, montada pela passada da retenção;
  `perf_alerts` (abertos e os fechados dos últimos 90 dias).
- **Painel:** a tela **Alertas** (abertos primeiro, depois os fechados, com o filtro por medida)
  e o contador no menu; a aba **Desempenho** do equipamento com as curvas de 24 horas (por
  minuto) e de 7 e 90 dias (por hora), desenhadas no servidor em SVG (o painel continua sem
  JavaScript), e os alertas da máquina.

## 6. Compatibilidade

- Agente anterior: não manda amostras; o servidor anterior ignora o campo novo do check-in
  (o decodificador aceita campos desconhecidos), e o agente novo ignora a falta dos limites
  (usa os padrões).
- O check-in leva no máximo 1.440 amostras (24 horas); um lote fora do formato é descartado com
  aviso no log, sem derrubar o check-in.

## 7. Fora desta versão

Disponibilidade (máquina crítica que parou de responder), serviços do Windows e do systemd,
saúde do disco (SMART), limites por grupo de máquinas, e-mail, Teams ou webhook, e integração
com sistema de chamados.
