# Relé na nuvem: notebooks fora da rede

Notebooks em casa ou em viagem não alcançam o servidor do patchd, que fica na rede interna. O
relé é uma máquina pequena na nuvem que **só repassa conexões**: não guarda dados, não tem
banco, certificado nem chave. O servidor, o banco e os arquivos continuam dentro da rede.

```
  Notebook fora ──TCP 443──► relé na nuvem ──túnel WireGuard──► servidor do patchd (rede interna)
                             (HAProxy, modo TCP)  (iniciado de dentro)  porta só de agentes :9443
  Notebook dentro ─────────────── TCP 443 direto (DNS interno) ──────► porta principal
```

- **O agente escolhe o caminho sozinho** (relé automático, seção 5): tenta o servidor direto e,
  sem resposta (fora da rede), disca o relé, com o TLS conferindo o nome do servidor; volta
  sozinho quando alcança o servidor de novo. Sem DNS público nem certificado novo.
- **Nenhuma porta de entrada na rede interna.** Quem abre o túnel é o servidor de dentro (UDP
  51820 de saída), com um pacote a cada 25 s que mantém a volta aberta no firewall.
- **TLS de ponta a ponta.** O relé repassa bytes cifrados; o aperto de mão é entre o agente e
  o servidor, com o certificado da CA interna (`-ca-file` no agente). A chave privada nunca
  sai da rede interna; um relé adulterado não se passa pelo servidor nem assina jobs.
- **Porta só de agentes** (`PATCHD_AGENT_LISTEN_ADDR`): a única que o túnel alcança. Atende as
  rotas do agente (`/api/v1/agent/...`, sempre com a credencial da máquina) e o `/healthz`;
  recusa o painel, a API de leitura e o registro de máquina nova (máquina nova se registra
  dentro da rede). As recusas vão para o log, no máximo uma linha por minuto.
- **Dentro ou fora:** o servidor guarda por qual porta chegou o último check-in; o Resumo da
  máquina mostra "Rede: fora da rede (pelo relé na nuvem)" ou "dentro da rede".
- **Canal em tempo real** atravessa o relé sem mudança (a espera de 45 s cabe nos 90 s do
  HAProxy).

Nos exemplos: `IP_DO_RELE` é o IP público da máquina na nuvem, `IP_DE_SAIDA` o IP com que a
rede interna sai para a internet (`curl -s https://checkip.amazonaws.com` no servidor) e
`patchd.exemplo` o nome do servidor. O túnel usa `10.99.0.1` (relé) e `10.99.0.2` (servidor).

## 0. Antes: a rede interna deixa sair?

No servidor do patchd:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://www.cloudflare.com     # HTTPS sai?
```

Com a máquina da nuvem criada (passo 1), o teste decisivo é o do UDP: na nuvem
`sudo nc -u -l 51820`; no servidor `echo oi | nc -u -w2 IP_DO_RELE 51820`. O "oi" aparecendo na
nuvem, o WireGuard funciona. Se só a UDP 443 passar, use `ListenPort = 443` e
`Endpoint = IP_DO_RELE:443` abaixo. Firewalls que inspecionam TLS costumam derrubar conexões
sem nome de site; isso não afeta o WireGuard (UDP) nem o agente (que sempre manda o nome).

## 1. A máquina na nuvem

Qualquer VM Linux pequena serve: o relé só repassa. Na Oracle Cloud, a `VM.Standard.E2.1.Micro`
(1 núcleo, 1 GB, Always Free) com Ubuntu, numa VCN com subnet **pública** e IP público.

- **Security List** (regras de entrada; a **porta de origem fica vazia**, só a de destino):

  | Origem | Protocolo | Destino | Para quê |
  |---|---|---|---|
  | `IP_DE_SAIDA/32` | UDP | 51820 | o túnel |
  | `0.0.0.0/0` | TCP | 443 | os notebooks, de qualquer lugar |
  | (padrão) | TCP | 22 | SSH de administração (restrinja quando der) |

- **Firewall do Ubuntu** (as imagens da Oracle bloqueiam tudo menos o SSH):

  ```bash
  sudo apt update && sudo apt install -y wireguard haproxy iptables-persistent
  sudo iptables -I INPUT -p udp --dport 51820 -j ACCEPT
  sudo iptables -I INPUT -p tcp --dport 443   -j ACCEPT
  sudo netfilter-persistent save
  ```

## 2. O túnel WireGuard

Cada ponta gera a sua chave; a privada não sai de onde nasceu, a pública é copiada para a
outra ponta.

```bash
sudo bash -c 'umask 077; wg genkey > /etc/wireguard/privada; wg pubkey < /etc/wireguard/privada > /etc/wireguard/publica'
sudo cat /etc/wireguard/publica
```

**Relé**, `/etc/wireguard/wg0.conf`:

```ini
[Interface]
Address    = 10.99.0.1/30
ListenPort = 51820
PostUp     = wg set %i private-key /etc/wireguard/privada

[Peer]
# servidor do patchd (rede interna)
PublicKey  = CHAVE_PUBLICA_DO_SERVIDOR
AllowedIPs = 10.99.0.2/32
```

**Servidor do patchd** (RHEL e derivados: `dnf install -y wireguard-tools`),
`/etc/wireguard/wg0.conf`:

```ini
[Interface]
Address    = 10.99.0.2/30
PostUp     = wg set %i private-key /etc/wireguard/privada

[Peer]
# relé na nuvem
PublicKey           = CHAVE_PUBLICA_DO_RELE
Endpoint            = IP_DO_RELE:51820
AllowedIPs          = 10.99.0.1/32
PersistentKeepalive = 25
```

`AllowedIPs` com um endereço só: o túnel não vira rota de nada além das duas pontas.

**Antes de ligar, feche o túnel no servidor**: tudo que vier pela `wg0` é descartado, menos a
porta só de agentes vinda do relé.

```bash
firewall-cmd --permanent --new-zone=rele
firewall-cmd --permanent --zone=rele --add-interface=wg0
firewall-cmd --permanent --zone=rele --set-target=DROP
firewall-cmd --permanent --zone=rele --add-rich-rule='rule family=ipv4 source address=10.99.0.1/32 port port=9443 protocol=tcp accept'
firewall-cmd --reload
firewall-cmd --zone=public --list-ports      # a 9443 NÃO deve aparecer aqui
```

Ligue nas duas pontas e confira:

```bash
sudo systemctl enable --now wg-quick@wg0
ping -c 3 10.99.0.1          # no servidor: resposta = túnel de pé
sudo wg show                 # "latest handshake" de menos de 2 minutos
```

## 3. A porta só de agentes no patchd

No `deploy/.env`:

```bash
PATCHD_AGENT_LISTEN_ADDR=:9443
PATCHD_AGENT_BIND=0.0.0.0     # quem limita é o firewall (zona rele); não amarre ao 10.99.0.2:
                              # com o túnel fora do ar na subida, o pod inteiro não subiria
PATCHD_AGENT_PORT=9443
```

Podman: `./deploy/podman/instalar.sh` e **reinicie o pod** (as portas publicadas são do pod,
definidas quando ele sobe; o `atualizar.sh` só reinicia o servidor):
`systemctl --user daemon-reload && systemctl --user restart patchd-pod.service`, depois
`./deploy/atualizar.sh` com a versão atual. No host, `ss -ltn | grep 9443` mostra a porta.
Compose: `docker compose up -d`. O log mostra `Agentes (relé) escutando`.

Do relé, confira o caminho e o filtro:

```bash
curl -sk -o /dev/null -w '%{http_code}\n' https://10.99.0.2:9443/healthz          # 200
curl -sk -o /dev/null -w '%{http_code}\n' https://10.99.0.2:9443/painel/entrar    # 403
```

## 4. O HAProxy no relé

`/etc/haproxy/haproxy.cfg` (o `global` e o `defaults` do pacote podem ficar; troque o resto):

```
frontend agentes
    bind :443
    mode tcp
    option tcplog
    timeout client 90s           # acima dos 45 s do canal em tempo real
    maxconn 4000
    stick-table type ip size 100k expire 1m store conn_rate(10s)
    tcp-request connection track-sc0 src
    tcp-request connection reject if { sc_conn_rate(0) gt 50 }
    default_backend patchd

backend patchd
    mode tcp
    timeout connect 5s
    timeout server 90s
    server servidor 10.99.0.2:9443 check inter 10s
```

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg && sudo systemctl enable --now haproxy && sudo systemctl restart haproxy
```

O HAProxy **não termina o TLS** (modo TCP): o servidor vê as conexões vindo de `10.99.0.1`; o
IP real de cada notebook fica no log do HAProxy (`journalctl -u haproxy`). O patchd identifica
e limita cada máquina pela credencial, não pelo IP.

## 5. Como o notebook chega ao relé

### Recomendado: relé automático (sem DNS público)

Em Configuração › Parâmetros › Agentes › **Relé para fora da rede** (`PATCHD_RELAY_ADDR`),
ponha o `IP_DO_RELE:PORTA` (a porta pública do HAProxy). O servidor manda o endereço em cada
check-in; o agente o guarda no disco (vale mesmo reiniciando fora da rede). A cada ciclo:

1. o agente tenta o servidor direto, com prazo de 3 s (fora da rede, o nome interno nem
   resolve);
2. sem resposta, **disca o relé**; respondendo de novo, **volta ao servidor direto**;
3. a escolha vale 1 minuto; a mudança de rede (IP novo) e uma falha de conexão fazem testar
   de novo na hora; o canal em tempo real também reavalia quando cai.

Só o endereço discado muda: o TLS continua com o **nome do servidor** (o da URL do agente,
mesmo sendo `.local`) e a CA interna. Não precisa de nome público, DNS externo nem certificado
novo; um endereço de relé adulterado só deixa o notebook sem contato. Requisitos: o agente
nesta versão (atualize **enquanto as máquinas estão dentro da rede**, para aprenderem o relé),
a URL do agente em HTTPS e **a mesma porta** no relé e no servidor (o HAProxy escuta na porta
que está na URL do agente; ex.: `bind :8443` se os agentes usam `:8443`).

### Alternativa: um nome público com DNS dividido

- **O mesmo nome dentro e fora:** no DNS público, `patchd.exemplo` → `IP_DO_RELE`; no DNS
  interno, o mesmo nome → o servidor. Um nome `.local` não serve: só existe no DNS interno.
- **A mesma porta dentro e fora:** o endereço do agente leva a porta (no Podman sem root, a 443
  no host pede `net.ipv4.ip_unprivileged_port_start=443` em `/etc/sysctl.d/`).
- **Certificado** da CA interna com o nome no SAN; `PATCHD_PUBLIC_URL` com nome e porta; os
  agentes instalados mudam de endereço pelo job `server_url`.

## 6. Teste de ponta a ponta

Num notebook com o agente desta versão, que já fez um check-in dentro da rede com o relé
configurado (ele aprende o endereço), conecte fora da rede (o 4G do celular serve):

```powershell
Test-NetConnection IP_DO_RELE -Port 8443   # TcpTestSucceeded : True
Restart-Service patchd-agent               # força um check-in agora
```

O log do agente mostra "rede: o servidor não responde direto; falando pelo relé"; voltando à
rede, "o servidor responde direto de novo".

No painel, a máquina passa a mostrar **Rede: fora da rede (pelo relé na nuvem)** depois do
próximo check-in; com o canal em tempo real ligado, "Tempo real: conectado". Um job pedido
para ela sai em segundos.

## Operação e falhas

| Situação | O que acontece | O que olhar |
|---|---|---|
| Relé fora do ar | Os notebooks de fora ficam sem contato e tentam de novo, com espera crescente; dentro da rede, nada muda | `systemctl status haproxy wg-quick@wg0` no relé |
| Túnel caído | Idem; o HAProxy marca o servidor como fora | `wg show` (handshake recente?), `ping 10.99.0.1` no servidor |
| IP de saída da rede interna mudou | O WireGuard se ajusta sozinho, mas a Security List só aceita o IP antigo | atualize a regra UDP 51820 (com várias saídas, todas) |
| Varredura da internet na 443 | O HAProxy repassa; a porta só de agentes recusa tudo que não é agente | `journalctl` do servidor: "recusado na porta só de agentes" |

**Segurança, em resumo:** o relé não guarda nada e não tem chave; um relé comprometido vê só
bytes cifrados, não se passa pelo servidor (CA interna no agente) e não manda comandos (jobs
assinados na rede interna). Pelo túnel, só a 9443 do servidor é alcançável (zona `rele` do
firewall); o painel nunca fica exposto.

**Tráfego:** check-ins e o canal em tempo real são poucos MB por notebook por dia; instaladores
e o catálogo offline pesam mais. Fora da rede, as atualizações do Windows podem vir direto da
Microsoft (Configuração › Parâmetros › Agentes › Fonte reserva do Windows).
