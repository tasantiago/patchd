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

Os limites ficam em **Configuração › Parâmetros › Monitoramento**, para a frota toda:

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
