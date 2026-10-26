#!/usr/bin/env bash
# Preparação da VM (RHEL 10) para o patchd no Podman sem root (Aula 23.1b). Como root, uma
# vez; rodar de novo só confere e completa o que faltar, sem desfazer nada.
#
#   bash deploy/podman/preparar-vm.sh [-u USUARIO] [-d PASTA] [-p PORTA] [--443] [--8080]
#
#   -u USUARIO  o usuário do serviço (padrão: patchd); criado se não existir
#   -d PASTA    onde o Podman do usuário guarda imagens, cache do build e volumes, o banco
#               inclusive (padrão: /srv/patchd). Fora do /home: numa VM com partições
#               separadas, o /home costuma ser pequeno (Aula 23.1: 10 GB, e só a primeira
#               subida usa de 4 a 5)
#   -p PORTA    a porta HTTPS no host, a PATCHD_TLS_PORT do .env (padrão: 8443)
#   --443       redireciona a 443 para a PORTA no firewall: o painel abre sem a porta na URL
#   --8080      abre a 8080 (HTTP de transição e cache de repositórios), se for usar
#
# Antes do clone do usuário do serviço, como root: dnf install -y git; git clone URL
# /tmp/patchd; bash /tmp/patchd/deploy/podman/preparar-vm.sh; rm -rf /tmp/patchd.
# Faz: pacotes; usuário com subuid/subgid e linger; diário do systemd em disco (sem ele, o
# usuário não lê o próprio log); a PASTA com o rótulo do SELinux da pasta padrão e o
# storage.conf do usuário apontando para ela; o firewall; e confere as portas.
set -euo pipefail

USUARIO=patchd
PASTA=/srv/patchd
PORTA=8443
P443=false
P8080=false
uso() { sed -n '5,14p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
while [ $# -gt 0 ]; do
  case "$1" in -u|-d|-p) [ $# -ge 2 ] || uso ;; esac
  case "$1" in
    -u) USUARIO="${2:-}"; shift 2 ;;
    -d) PASTA="${2:-}"; shift 2 ;;
    -p) PORTA="${2:-}"; shift 2 ;;
    --443) P443=true; shift ;;
    --8080) P8080=true; shift ;;
    *) uso ;;
  esac
done
[[ "$USUARIO" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || { echo "-u: nome de usuário inválido" >&2; exit 2; }
[[ "$PASTA" =~ ^/[A-Za-z0-9._/-]+$ ]] && [[ "$PASTA" != /home/* ]] ||
  { echo "-d: um caminho absoluto, fora do /home, só com letras, números, ., _, - e /" >&2; exit 2; }
[[ "$PORTA" =~ ^[0-9]{1,5}$ ]] && [ "$PORTA" -ge 1024 ] && [ "$PORTA" -le 65535 ] ||
  { echo "-p: uma porta de 1024 a 65535 (sem root, o pod não abre porta baixa; use --443)" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "rode como root" >&2; exit 1; }
grep -qE '^(ID|ID_LIKE)=.*(rhel|fedora|centos)' /etc/os-release 2>/dev/null ||
  echo "AVISO: feito para o RHEL; outro sistema pode precisar de ajustes" >&2

feito() { echo "  ok  $*"; }
aviso() { echo "  !!  $*" >&2; }

echo "== pacotes"
# passt: a rede pasta do pod. policycoreutils-python-utils: o semanage, para a PASTA.
faltam=()
for p in podman git openssl acl passt policycoreutils-python-utils; do
  rpm -q "$p" >/dev/null 2>&1 || faltam+=("$p")
done
if [ ${#faltam[@]} -gt 0 ]; then
  dnf install -y -q "${faltam[@]}"
  feito "instalados: ${faltam[*]}"
else
  feito "todos presentes"
fi

echo "== usuário $USUARIO"
if id "$USUARIO" >/dev/null 2>&1; then
  feito "já existe"
else
  useradd -m "$USUARIO"
  feito "criado (sem senha: entre com sudo -iu $USUARIO)"
fi
for f in /etc/subuid /etc/subgid; do
  # Faixa de UIDs do usuário para os containers; o useradd do RHEL já reserva 65536.
  n="$(awk -F: -v u="$USUARIO" '$1==u {s+=$3} END {print s+0}' "$f")"
  if [ "$n" -lt 65536 ]; then
    aviso "$f: o usuário tem $n UIDs reservados; precisa de 65536 (usermod --add-subuids/--add-subgids)"
    exit 1
  fi
done
feito "subuid e subgid com 65536 ou mais"
loginctl enable-linger "$USUARIO"
feito "linger: os serviços dele sobem no boot, sem login"

echo "== diário do systemd"
# Só em memória (/run), o diário não separa por usuário, e o usuário do serviço não lê o
# log dos próprios serviços (journalctl --user: "insufficient permissions").
if [ -d /var/log/journal ]; then
  feito "já em disco (/var/log/journal)"
else
  mkdir -p /var/log/journal
  systemd-tmpfiles --create --prefix /var/log/journal
  systemctl restart systemd-journald
  feito "agora em disco (/var/log/journal)"
fi

echo "== armazenamento do Podman em $PASTA"
casa="$(getent passwd "$USUARIO" | cut -d: -f6)"
padrao="$casa/.local/share/containers"
install -d -m 700 -o "$USUARIO" -g "$USUARIO" "$PASTA" "$PASTA/containers"
# Os mesmos rótulos do SELinux da pasta padrão; sem isso, o container não lê as camadas.
if semanage fcontext -l -C | grep -qF "$PASTA/containers = "; then
  feito "regra do SELinux já existe"
else
  semanage fcontext -a -e "$padrao" "$PASTA/containers"
  feito "regra do SELinux: $PASTA/containers = $padrao"
fi
restorecon -R "$PASTA/containers"
conf="$casa/.config/containers/storage.conf"
graph="$PASTA/containers/storage"
if [ -e "$conf" ]; then
  if grep -qF "\"$graph\"" "$conf"; then
    feito "storage.conf já aponta para $graph"
  else
    aviso "$conf já existe e aponta para outro lugar; não mexi (graphroot = \"$graph\")"
  fi
else
  install -d -m 700 -o "$USUARIO" -g "$USUARIO" "$casa/.config" "$casa/.config/containers"
  printf '# Aula 23.1b (preparar-vm.sh): imagens, cache e volumes fora do /home.\n[storage]\ndriver = "overlay"\ngraphroot = "%s"\n' "$graph" > "$conf"
  chown "$USUARIO:$USUARIO" "$conf"
  feito "storage.conf: graphroot = $graph"
fi
if [ -n "$(ls -A "$padrao/storage" 2>/dev/null || true)" ]; then
  aviso "já há imagens na pasta padrão ($padrao/storage): como $USUARIO, podman system reset (apaga) antes de seguir"
fi
livre="$(df -Pk "$PASTA" | awk 'NR==2 {print int($4/1048576)}')"
if [ "$livre" -lt 15 ]; then
  aviso "só ${livre} GB livres em $PASTA (a primeira subida usa de 4 a 5 GB, e o banco cresce)"
else
  feito "${livre} GB livres em $PASTA"
fi

echo "== firewall"
if systemctl is-active --quiet firewalld; then
  firewall-cmd -q --permanent --add-port="$PORTA/tcp"
  feito "porta $PORTA/tcp aberta"
  if $P443; then
    firewall-cmd -q --permanent --add-forward-port="port=443:proto=tcp:toport=$PORTA"
    feito "443 redirecionada para $PORTA (painel sem a porta na URL; PATCHD_PUBLIC_URL sem a porta)"
  fi
  if $P8080; then
    firewall-cmd -q --permanent --add-port=8080/tcp
    feito "porta 8080/tcp aberta"
  fi
  firewall-cmd -q --reload
else
  aviso "firewalld parado: libere a porta $PORTA/tcp no firewall que estiver em uso"
fi

echo "== portas no host"
# O pod publica a HTTPS, a HTTP (8080) e a das métricas (9090, só em 127.0.0.1). Ocupada,
# o pod não sobe, e o atualizar.sh diz "o Postgres não subiu".
for p in "$PORTA" 8080 9090; do
  quem="$(ss -Hltnp "( sport = :$p )" 2>/dev/null | grep -o 'users:(("[^"]*"' | head -1 | cut -d'"' -f2 || true)"
  if [ -z "$(ss -Hltn "( sport = :$p )" 2>/dev/null)" ]; then
    feito "$p livre"
  elif [ "$p" = 9090 ]; then
    aviso "9090 em uso (${quem:-?}; no RHEL, o Cockpit): no deploy/.env, PATCHD_METRICS_PORT=9091"
  elif [ "$p" = 8080 ]; then
    aviso "8080 em uso (${quem:-?}): no deploy/.env, PATCHD_HTTP_PORT=8081 (ou outra livre)"
  else
    aviso "$p em uso (${quem:-?}): escolha outra com -p e use a mesma em PATCHD_TLS_PORT"
  fi
done

cat <<MSG
pronto. Agora, como o usuário do serviço (docs/podman.md, "Instalação"):
  sudo -iu $USUARIO
  git clone URL_DO_REPOSITORIO patchd && cd patchd && ls deploy/podman
MSG
