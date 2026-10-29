#!/usr/bin/env bash
# patchd no Podman sem root, com Quadlet (Aula 23.1): gera as unidades do systemd do usuário a
# partir dos modelos desta pasta e do deploy/.env.
#
#   ./deploy/podman/instalar.sh          gera (ou regera) as unidades e recarrega o systemd
#   ./deploy/podman/instalar.sh --env    só mostra as variáveis do servidor (não grava nada)
#
# Rode como o usuário do serviço (nunca root) e de novo a cada mudança no deploy/.env ou nos
# modelos. Não sobe nada: a primeira subida e as trocas de versão são do ./deploy/atualizar.sh.
#
# Gera, em ~/.config: containers/systemd/patchd.pod, patchd-postgres.container,
# patchd-server.container e os volumes (o Quadlet faz deles os serviços patchd-pod,
# patchd-postgres e patchd-server); patchd/servidor.env, as variáveis do servidor; e
# systemd/user/patchd-backup.{service,timer}, a cópia diária.
#
# As variáveis do servidor seguem o deploy/compose.yaml, com os mesmos padrões; o
# deploy/podman/podman_test.go confere que nenhuma ficou para trás.
set -euo pipefail
cd "$(dirname "$0")/../.."
REPO="$(pwd)"

so_env=false
case "${1:-}" in
  --env) so_env=true ;;
  "") ;;
  *) sed -n '4,5p' "$0" | sed 's/^# //' >&2; exit 2 ;;
esac

[ -s deploy/.env ] || { echo "falta deploy/.env (cp deploy/.env.exemplo deploy/.env e preencha)" >&2; exit 1; }
COMUM_SO_LEITURA=true   # a conferência do usuário e do systemd vem abaixo, depois do --env
. deploy/comum.sh

# val CHAVE: o valor no deploy/.env, sem as aspas em volta (o compose também tira) e sem o
# \r de um arquivo editado no Windows.
val() {
  local v
  v="$(env_get "$1" | tr -d '\r')"
  case "$v" in
    \"*\") v="${v#\"}"; v="${v%\"}" ;;
    \'*\') v="${v#\'}"; v="${v%\'}" ;;
  esac
  printf '%s' "$v"
}

# As quatro formas do compose.yaml:
fixo() { printf '%s=%s\n' "$1" "$2"; }                                   # VALOR
padrao() { local v; v="$(val "$1")"; printf '%s=%s\n' "$1" "${v:-$2}"; }  # ${X:-padrão}
ausente() {                                                              # ${X-padrão}
  if env_tem "$1"; then printf '%s=%s\n' "$1" "$(val "$1")"; else printf '%s=%s\n' "$1" "$2"; fi
}
obrigatoria() {                                                          # ${X:?erro}
  local v; v="$(val "$1")"
  [ -n "$v" ] || { echo "defina $1 em deploy/.env" >&2; exit 1; }
  printf '%s=%s\n' "$1" "$v"
}
# Fora do compose: o proxy de saída do servidor (catálogos e fontes), só se estiver no .env.
repassa() { local v; v="$(val "$1")"; [ -z "$v" ] || printf '%s=%s\n' "$1" "$v"; }

servidor_env() {
  fixo PATCHD_LOG_FORMAT json
  padrao PATCHD_LOG_LEVEL info
  padrao PATCHD_TIMEZONE America/Porto_Velho
  padrao PATCHD_PANEL_TITLE patchd
  fixo PATCHD_DATABASE_URL_FILE /run/secrets/database_url
  fixo PATCHD_LISTEN_ADDR :8443
  fixo PATCHD_TLS_CERT_FILE /run/patchd/tls/server.crt
  fixo PATCHD_TLS_KEY_FILE /run/patchd/tls/server.key
  ausente PATCHD_HTTP_LISTEN_ADDR :8080
  padrao PATCHD_HTTP_CACHE_ONLY false
  obrigatoria PATCHD_PUBLIC_URL
  padrao PATCHD_AGENT_CA_FILE ""
  fixo PATCHD_JOB_SIGNING_KEY_FILE /run/secrets/job_signing_key
  padrao PATCHD_BIOS_ESCROW_KEY_FILE ""
  padrao PATCHD_DISCOVERY_KEY_FILE ""
  fixo PATCHD_RELEASES_DIR /var/lib/patchd/releases
  fixo PATCHD_CONTENT_DIR /var/lib/patchd/conteudo
  fixo PATCHD_REPO_CACHE_DIR /var/lib/patchd/repocache
  padrao PATCHD_REPO_CACHE_URL ""
  padrao PATCHD_LDAP_HOST ""
  padrao PATCHD_LDAP_DOMAIN ""
  padrao PATCHD_LDAP_BASE_DN ""
  padrao PATCHD_LDAP_ADMIN_GROUP ""
  padrao PATCHD_LDAP_READ_GROUP ""
  padrao PATCHD_OIDC_ISSUER ""
  padrao PATCHD_OIDC_CLIENT_ID patchd-painel
  fixo PATCHD_OIDC_CLIENT_SECRET_FILE /run/secrets/oidc_client_secret
  padrao PATCHD_OIDC_ROLES_CLAIM groups
  padrao PATCHD_OIDC_ADMIN_ROLE patchd-admin
  padrao PATCHD_OIDC_READ_ROLE patchd-leitura
  padrao PATCHD_RETENTION_HISTORY_DAYS 90
  padrao PATCHD_RETENTION_JOBS_DAYS 400
  ausente PATCHD_METRICS_LISTEN_ADDR :9090
  fixo PATCHD_METRICS_TOKEN_FILE /run/secrets/metrics_token
  padrao PATCHD_SMTP_ADDR ""
  padrao PATCHD_SMTP_FROM ""
  padrao PATCHD_NOTIFY_TO ""
  padrao PATCHD_SMTP_TLS starttls
  padrao PATCHD_SMTP_USER ""
  fixo PATCHD_SMTP_PASSWORD_FILE /run/secrets/smtp_password
  padrao PATCHD_SMTP_CA_FILE ""
  padrao PATCHD_NOTIFY_EVENTS ""
  repassa HTTPS_PROXY
  repassa HTTP_PROXY
  repassa NO_PROXY
}

# Monta tudo antes de gravar qualquer coisa: um erro no .env não deixa meia instalação.
envtxt="$(servidor_env)"
if $so_env; then
  printf '%s\n' "$envtxt"
  exit 0
fi

[ "$MOTOR" = podman ] || [ -z "$(env_get PATCHD_MOTOR)" ] ||
  { echo "deploy/.env diz PATCHD_MOTOR=$MOTOR; este script é do Podman" >&2; exit 1; }
[ "$(id -u)" != 0 ] || { echo "rode como o usuário do serviço (sudo -iu USUARIO), não como root" >&2; exit 1; }
if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -d "/run/user/$(id -u)" ]; then
  export XDG_RUNTIME_DIR="/run/user/$(id -u)"
fi
command -v podman >/dev/null || { echo "falta o podman (como root: dnf install podman)" >&2; exit 1; }

# Podman 5 ou mais novo: Notify=healthy e as unidades .pod do Quadlet (o RHEL 10 já vem com ele).
pv="$(podman version --format '{{.Client.Version}}' 2>/dev/null || true)"
case "$pv" in
  [5-9].*|[1-9][0-9].*) ;;
  *) echo "Podman ${pv:-desconhecido}: precisa do 5.0 ou mais novo" >&2; exit 1 ;;
esac
systemctl --user show-environment >/dev/null 2>&1 ||
  { echo "o systemd deste usuário não responde (entre com sudo -iu USUARIO ou machinectl shell USUARIO@)" >&2; exit 1; }
if [ "$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)" != yes ]; then
  echo "AVISO: sem linger, os serviços param quando você sai e não sobem com o boot." >&2
  echo "  como root: loginctl enable-linger $(id -un) (ou deploy/podman/preparar-vm.sh)" >&2
fi
# Aula 23.1b: com o diário só em memória, o journalctl --user não lê nada ("insufficient
# permissions"), e os logs que os scripts mostram somem.
if [ ! -d /var/log/journal ]; then
  echo "AVISO: o diário do systemd está só em memória; o journalctl --user não vai ler os logs do patchd." >&2
  echo "  como root: mkdir -p /var/log/journal && systemd-tmpfiles --create --prefix /var/log/journal && systemctl restart systemd-journald" >&2
fi
# Aula 23.1b: imagens, cache do build e volumes (o banco) ficam no armazenamento do Podman,
# por padrão no /home, que numa VM com partições separadas costuma ser pequeno.
graph="$(podman info --format '{{.Store.GraphRoot}}' 2>/dev/null || true)"
if [ -n "$graph" ]; then
  d="$graph"; while [ ! -d "$d" ] && [ "$d" != / ]; do d="$(dirname "$d")"; done
  livre="$(df -Pk "$d" | awk 'NR==2 {print int($4/1048576)}')"
  if [ "$livre" -lt 15 ]; then
    echo "AVISO: só ${livre} GB livres no armazenamento do Podman ($graph); a primeira subida usa de 4 a 5 GB e o banco cresce." >&2
    echo "  mude para outra partição antes da primeira subida: docs/podman.md, \"Disco\" (ou deploy/podman/preparar-vm.sh)" >&2
  fi
fi

# Caminhos e endereços entram nas unidades: só caracteres que o systemd não interpreta.
caminho() { # CHAVE PADRÃO: pasta do .env, relativa a deploy/ (como no compose)
  local v; v="$(val "$1")"; v="${v:-$2}"
  case "$v" in /*) ;; *) v="$REPO/deploy/$v" ;; esac
  v="$(realpath -m "$v")"
  [[ "$v" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "$1: use um caminho só com letras, números, ., _, - e / ($v)" >&2; exit 1; }
  printf '%s' "$v"
}
porta() { # CHAVE PADRÃO
  local v; v="$(val "$1")"; v="${v:-$2}"
  [[ "$v" =~ ^[0-9]{1,5}$ ]] && [ "$v" -ge 1 ] && [ "$v" -le 65535 ] || { echo "$1: porta inválida ($v)" >&2; exit 1; }
  printf '%s' "$v"
}
endereco() { # CHAVE PADRÃO: IPv4 ou IPv6 entre colchetes
  local v; v="$(val "$1")"; v="${v:-$2}"
  [[ "$v" =~ ^([0-9.]+|\[[0-9A-Fa-f:]+\])$ ]] || { echo "$1: use um IP ($v)" >&2; exit 1; }
  printf '%s' "$v"
}
[[ "$REPO" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "a pasta do repositório tem caracteres que o systemd interpreta: $REPO" >&2; exit 1; }

CONF="${XDG_CONFIG_HOME:-$HOME/.config}"
QDIR="$CONF/containers/systemd"
UDIR="$CONF/systemd/user"
ENV_FILE="$CONF/patchd/servidor.env"
SECRETS="$REPO/deploy/secrets"
TLS_DIR="$(caminho PATCHD_TLS_DIR ./tls)"
RELEASES_DIR="$(caminho PATCHD_RELEASES_HOST_DIR ../releases)"
DISCOVERY_KEY_FILE="$(caminho PATCHD_DISCOVERY_KEY_HOST_FILE /dev/null)"
TLS_BIND="$(endereco PATCHD_TLS_BIND 0.0.0.0)"; TLS_PORT="$(porta PATCHD_TLS_PORT 8443)"
HTTP_BIND="$(endereco PATCHD_HTTP_BIND 0.0.0.0)"; HTTP_PORT="$(porta PATCHD_HTTP_PORT 8080)"
METRICS_BIND="$(endereco PATCHD_METRICS_BIND 127.0.0.1)"; METRICS_PORT="$(porta PATCHD_METRICS_PORT 9090)"
[[ "$CONF" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "XDG_CONFIG_HOME com caracteres que o systemd interpreta: $CONF" >&2; exit 1; }

# Aula 23.1b: uma porta ocupada no host derruba o pod inteiro, e o que aparece é "o Postgres
# não subiu". Com o pod no ar, as portas são dele: não há o que conferir.
if command -v ss >/dev/null && ! systemctl --user is-active --quiet patchd-pod.service; then
  ocupadas=""
  for par in "PATCHD_TLS_PORT:$TLS_PORT" "PATCHD_HTTP_PORT:$HTTP_PORT" "PATCHD_METRICS_PORT:$METRICS_PORT"; do
    if [ -n "$(ss -Hltn "( sport = :${par#*:} )" 2>/dev/null)" ]; then
      ocupadas="$ocupadas ${par%%:*}=${par#*:}"
    fi
  done
  if [ -n "$ocupadas" ]; then
    echo "portas já em uso no host:$ocupadas" >&2
    echo "  escolha outras no deploy/.env (no RHEL, a 9090 costuma ser o Cockpit: PATCHD_METRICS_PORT=9091) e rode de novo" >&2
    exit 1
  fi
fi

# O Podman não cria a origem de um bind mount (o Docker cria): as pastas existem antes.
mkdir -p "$RELEASES_DIR" "$SECRETS" "$TLS_DIR"
chmod 700 "$SECRETS" "$TLS_DIR"
mkdir -p "$QDIR" "$UDIR"
( umask 077; mkdir -p "$(dirname "$ENV_FILE")"; printf '%s\n' "$envtxt" > "$ENV_FILE.novo" )
mv "$ENV_FILE.novo" "$ENV_FILE"

modelo() { # ARQUIVO DESTINO
  sed -e "s|@REPO@|$REPO|g" -e "s|@SECRETS@|$SECRETS|g" -e "s|@ENV_FILE@|$ENV_FILE|g" \
      -e "s|@DISCOVERY_KEY_FILE@|$DISCOVERY_KEY_FILE|g" \
      -e "s|@TLS_DIR@|$TLS_DIR|g" -e "s|@RELEASES_DIR@|$RELEASES_DIR|g" \
      -e "s|@TLS_BIND@|$TLS_BIND|g" -e "s|@TLS_PORT@|$TLS_PORT|g" \
      -e "s|@HTTP_BIND@|$HTTP_BIND|g" -e "s|@HTTP_PORT@|$HTTP_PORT|g" \
      -e "s|@METRICS_BIND@|$METRICS_BIND|g" -e "s|@METRICS_PORT@|$METRICS_PORT|g" "$1" > "$2.novo"
  if grep -q '@[A-Z_]*@' "$2.novo"; then
    echo "modelo com marcador sem valor: $1" >&2; rm -f "$2.novo"; exit 1
  fi
  mv "$2.novo" "$2"
}
for f in patchd.pod patchd-postgres.container patchd-server.container \
         patchd-pgdata.volume patchd-conteudo.volume patchd-repocache.volume; do
  modelo "deploy/podman/$f" "$QDIR/$f"
done
for f in patchd-backup.service patchd-backup.timer; do
  modelo "deploy/podman/$f" "$UDIR/$f"
done

# O gerador do Quadlet valida as unidades; um erro aqui seria um serviço que some em silêncio.
for q in /usr/libexec/podman/quadlet /usr/lib/podman/quadlet; do
  if [ -x "$q" ]; then
    saida="$("$q" -dryrun -user 2>&1 >/dev/null)" || { printf '%s\n' "$saida" | grep -v 'Loading source unit file' >&2; echo "o Quadlet recusou as unidades (acima)" >&2; exit 1; }
    break
  fi
done
systemctl --user daemon-reload
for s in patchd-pod patchd-postgres patchd-server; do
  systemctl --user cat "$s.service" >/dev/null 2>&1 ||
    { echo "o systemd não gerou $s.service: /usr/libexec/podman/quadlet -dryrun -user" >&2; exit 1; }
done
[ "$MOTOR" = podman ] || env_set PATCHD_MOTOR podman

# A imagem do banco vem antes da primeira subida: um registro ou proxy fora do ar aparece
# aqui, com a mensagem do podman, e não como um serviço que não sobe.
pgimg="$(sed -n 's/^Image=//p' deploy/podman/patchd-postgres.container)"
if ! podman image exists "$pgimg"; then
  echo "baixando $pgimg"
  podman pull -q "$pgimg" >/dev/null ||
    { echo "não consegui baixar $pgimg (proxy? registro bloqueado? docs/podman.md)" >&2; exit 1; }
fi

cat <<MSG
unidades geradas em $QDIR e $UDIR; variáveis do servidor em $ENV_FILE
  portas no host: $TLS_BIND:$TLS_PORT (HTTPS), $HTTP_BIND:$HTTP_PORT (HTTP), $METRICS_BIND:$METRICS_PORT (métricas)
  deploy/.env: PATCHD_MOTOR=podman (os scripts do deploy/ passam a usar o Podman)
próximos passos: ./deploy/gerar-segredos.sh e, com tudo presente, ./deploy/atualizar.sh vX.Y.Z
depois da primeira subida: systemctl --user enable --now patchd-backup.timer
MSG
