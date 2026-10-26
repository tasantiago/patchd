# patchd no RHEL com Podman (Aula 23.1)

O mesmo servidor do `deploy/compose.yaml`, no Podman sem root e com unidades do systemd geradas
pelo Quadlet: o caminho que a Red Hat documenta e suporta no RHEL 10. Nada aqui tem nome de
host, IP ou segredo do ambiente real: esses valores ficam no `deploy/.env` e em
`deploy/secrets`, fora do git.

## Como fica

- Um **usuário do serviço** comum (sem sudo) é dono do clone do repositório, das imagens, dos
  volumes e das unidades. Com *linger*, o systemd dele sobe os serviços no boot, sem ninguém
  logado.
- Um **pod** `patchd` com dois containers: `patchd-postgres` (a imagem oficial do Postgres, a
  mesma do compose) e `patchd-server` (a imagem do `deploy/Dockerfile`). Os dois dividem a rede
  do pod: o servidor fala com o banco em 127.0.0.1 (o nome `postgres` da URL do banco aponta
  para lá), e o banco não tem porta no host.
- **Rede pasta**, não bridge: sem root, uma rede bridge passa as conexões por um proxy que troca
  o IP de origem, e todas as máquinas chegariam com o mesmo IP. O "último IP" de cada máquina,
  o Wake-on-LAN, a trava de login por IP e a auditoria precisam do IP de verdade.
- **keep-id**: o usuário do serviço vira o UID 65532 lá dentro, o do servidor. Os segredos e o
  TLS são arquivos dele, `600`, lidos sem ACL. O root dos containers é um UID sem poder no host.
- **SELinux** ligado: as montagens levam `:Z` (rótulo só daquele pod).
- Serviços do systemd do usuário: `patchd-pod`, `patchd-postgres`, `patchd-server` e o timer
  `patchd-backup`. Com `Notify=healthy`, o systemd só dá o banco e o servidor por iniciados
  quando o healthcheck passa.

O `PATCHD_MOTOR=podman` no `deploy/.env` faz os scripts do `deploy/` usarem o Podman: o
`atualizar.sh`, o `backup.sh`, o `restaurar.sh` e o `gerar-segredos.sh` são os mesmos, com os
mesmos comandos. O `compose.sh` não se aplica (ele mostra os equivalentes).

## Preparação da VM (como root, uma vez)

```sh
dnf install -y podman git openssl acl     # RHEL 10: Podman 5, com o Quadlet
useradd -m patchd                         # o usuário do serviço; o useradd já reserva os subuid/subgid
grep patchd /etc/subuid /etc/subgid       # uma faixa de 65536 em cada
loginctl enable-linger patchd             # serviços do usuário no boot, sem login
firewall-cmd --permanent --add-port=8443/tcp   # HTTPS: painel e agentes
firewall-cmd --permanent --add-port=8080/tcp   # HTTP de transição e cache (só se usar)
firewall-cmd --reload
```

O nome `patchd` do usuário é um exemplo; o resto do guia chama de "usuário do serviço".
A porta das métricas (9090) fica só no próprio host; para um Prometheus de outra máquina,
libere só o IP dele (regra rica do firewalld), não a porta para a rede inteira.

**Proxy de saída**, se a rede exigir: o usuário do serviço precisa dele para baixar as imagens
e os módulos do Go no build. No `~/.bashrc` do usuário do serviço:

```sh
export HTTPS_PROXY=http://PROXY:PORTA HTTP_PROXY=http://PROXY:PORTA NO_PROXY=localhost,127.0.0.1,.DOMINIO_INTERNO
```

O servidor, para buscar os catálogos, recebe o proxy pelo `deploy/.env` (`HTTPS_PROXY`,
`NO_PROXY`; o `NO_PROXY` com os nomes internos: AD, RHBK, SMTP). Proxy com inspeção TLS: a CA
dele, em PEM, em `.docker/certs/` (o build confia nela e a leva para a imagem).

**Registro de imagens**: o build baixa `docker.io/library/golang` e o pod,
`docker.io/library/postgres`. Se o Docker Hub estiver bloqueado, peça à infra o espelho
interno e configure-o em `/etc/containers/registries.conf.d/` (um `[[registry]]` com
`location = "docker.io"` e o `[[registry.mirror]]` do espelho).

## Instalação (como o usuário do serviço)

Entre com `sudo -iu patchd` (o `XDG_RUNTIME_DIR` é ajustado pelos scripts) ou
`machinectl shell patchd@` (sessão completa; pacote `systemd-container`).

```sh
git clone URL_DO_REPOSITORIO patchd && cd patchd     # ou de um git bundle, se o GitHub for bloqueado
cp deploy/.env.exemplo deploy/.env && vi deploy/.env  # PATCHD_PUBLIC_URL, no mínimo
./deploy/podman/instalar.sh     # unidades do Quadlet, PATCHD_MOTOR=podman, imagem do Postgres
./deploy/gerar-segredos.sh      # banco, token das métricas; diz o que falta
#   TLS: server.crt e server.key em deploy/tls (da CA interna)
./deploy/atualizar.sh vX.Y.Z    # constrói, sobe o banco e o servidor e confere
systemctl --user enable --now patchd-backup.timer
```

Na primeira subida de uma frota nova, o `atualizar.sh` constrói a imagem e para antes de
subir, pedindo a chave de jobs: ele mostra o `podman run ... job keygen` com a imagem recém
construída (não há Go na VM). Gere, guarde a pública para o build dos agentes e rode o
`atualizar.sh` de novo; a construção sai do cache. Com o banco vazio, não há cópia antes da
troca (nem volta).

Mudou o `deploy/.env` (portas, LDAP, SMTP...)? `./deploy/podman/instalar.sh` de novo e
`systemctl --user restart patchd-server`. Mudou a versão? `./deploy/atualizar.sh`, como no compose.

## Operação

| No compose | No Podman |
|---|---|
| `./deploy/compose.sh ps` | `systemctl --user status patchd-server patchd-postgres` |
| `./deploy/compose.sh logs --tail 50 server` | `journalctl --user -u patchd-server -n 50` |
| `./deploy/compose.sh exec server /patchd-server ARGS` | `podman exec patchd-server /patchd-server ARGS` |
| `./deploy/compose.sh restart server` | `systemctl --user restart patchd-server` |
| `./deploy/compose.sh up -d` | `systemctl --user start patchd-server` |
| `docker image ls patchd-server` | `podman image ls patchd-server` |
| timer `patchd-backup` do sistema (`deploy/systemd`) | timer `patchd-backup` do usuário (`systemctl --user list-timers`) |

A versão no ar é a imagem `localhost/patchd-server:atual`; o `atualizar.sh` troca essa tag
e as versões ficam como `localhost/patchd-server:vX.Y.Z`, para a volta (`--voltar`).

## Problemas comuns

- **"short-name resolution enforced but cannot prompt without a TTY"**: uma imagem sem o
  registro no nome. As do repositório já vêm com `docker.io/library/...`.
- **`Failed to connect to bus`** no `systemctl --user`: sessão sem `XDG_RUNTIME_DIR`. Use
  `sudo -iu USUARIO` e os scripts do `deploy/`, ou `export XDG_RUNTIME_DIR=/run/user/$(id -u)`.
- **Serviços param quando o usuário sai** ou não sobem no boot: falta o linger
  (`loginctl enable-linger USUARIO`, como root).
- **Porta já em uso** (o pod não sobe; o log fala em `Failed to bind port`): outra coisa no
  host usa a porta. Mude `PATCHD_TLS_PORT`/`PATCHD_HTTP_PORT` no `.env` e rode o `instalar.sh`.
- **Permissão negada lendo um segredo**: confira o dono (o usuário do serviço) e o SELinux
  (`ausearch -m avc -ts recent`, como root). Os `:Z` das unidades rotulam a cada subida.
- **O servidor não fica saudável**: `journalctl --user -u patchd-server -n 50`. O
  `atualizar.sh` para, mostra o fim do log e diz o comando da volta.
- **Root não vê nada**: `podman ps` como root olha outro armazenamento. Tudo do patchd é do
  usuário do serviço.
