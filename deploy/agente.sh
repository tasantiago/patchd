#!/usr/bin/env bash
# Agente feito no próprio servidor, no Podman (Aula 23.2): compila, assina e publica uma
# versão do agente sem o ambiente de desenvolvimento e sem Go instalado no host.
#
#   ./deploy/agente.sh VERSAO -key ARQUIVO   compila o commit atual num container do Go,
#                                            assina com a chave de release e publica
#   ./deploy/agente.sh --publicar VERSAO     publica de novo uma versão que já está aqui
#                                            (para voltar a uma anterior, por exemplo)
#
# A chave de release NÃO mora no servidor: quem a tivesse aqui assinaria um "agente"
# qualquer para a frota inteira. Ela vem só na hora, para a memória (/dev/shm), e sai no
# fim: do seu computador, scp ARQUIVO SEU_USUARIO@SERVIDOR:/dev/shm/ e, como root,
# chown USUARIO_DO_SERVICO /dev/shm/ARQUIVO. Dentro do container ela fica num tmpfs.
#
# As chaves públicas embutidas nos agentes vêm de onde já estão: a de jobs, do servidor no ar
# (patchd-server job pubkey); a de release, da própria chave que assina. As seguintes (Aula
# 20.7) e o nome da organização (Aula 20.9) vêm do deploy/.env, se houver:
# PATCHD_UPDATE_PUBKEY_NEXT, PATCHD_JOB_PUBKEY_NEXT e PATCHD_APP_COMPANY.
# O agente não pode ser mais novo que o servidor: atualize o servidor antes.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="$(pwd)"
. deploy/comum.sh

uso() { sed -n '5,9p' "$0" | sed 's/^# //' >&2; exit 2; }
[ "$MOTOR" = podman ] || {
  echo "PATCHD_MOTOR=$MOTOR: este script é do servidor no Podman; no compose, o agente sai do scripts/build.sh e do patchd-release (docs/operacao.md)" >&2
  exit 1
}
versao_ok() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; }
val() { env_get "$1" | tr -d '\r"'"'"; }

# A pasta de versões do host, a mesma que o instalar.sh monta no servidor.
rel="$(val PATCHD_RELEASES_HOST_DIR)"; rel="${rel:-../releases}"
case "$rel" in /*) ;; *) rel="$REPO/deploy/$rel" ;; esac
RELEASES="$(realpath -m "$rel")"
mkdir -p "$RELEASES"

# publicar VERSAO: o mesmo "release publish" do servidor, num container avulso da imagem em
# uso (o servidor no ar só lê a pasta). :z, e não :Z: o rótulo compartilhado deixa o
# servidor no ar continuar lendo a pasta.
publicar() {
  podman run --rm --userns keep-id:uid=65532,gid=65532 \
    -v "$RELEASES:/var/lib/patchd/releases:z" -e PATCHD_RELEASES_DIR=/var/lib/patchd/releases \
    "$IMAGEM:atual" release publish "$1"
}

if [ "${1:-}" = --publicar ]; then
  [ $# -eq 2 ] && versao_ok "$2" || uso
  [ -s "$RELEASES/$2/manifest.json" ] || { echo "a versão $2 não está em $RELEASES" >&2; exit 1; }
  publicar "$2"
  exit 0
fi

[ $# -eq 3 ] && [ "$2" = -key ] || uso
VERSAO="$1"; KEY="$3"
versao_ok "$VERSAO" || { echo "versão inválida: $VERSAO (ex.: v0.18.7)" >&2; exit 2; }
[ -r "$KEY" ] && [ -s "$KEY" ] || { echo "não consigo ler a chave $KEY (o dono tem de ser $(id -un): chown, como root)" >&2; exit 1; }
case "$(realpath "$KEY")/" in
  "$REPO"/*) echo "a chave de release não pode ficar dentro do repositório; use /dev/shm" >&2; exit 1 ;;
esac
[ ! -e "$RELEASES/$VERSAO" ] ||
  { echo "a versão $VERSAO já existe em $RELEASES (uma versão assinada não é refeita; para publicá-la de novo: --publicar)" >&2; exit 1; }
# O binário do agente leva o commit: com alterações não commitadas, ele não corresponderia
# a commit nenhum.
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "há alterações não commitadas no repositório" >&2; exit 1; }

# O servidor no ar: a chave pública de jobs e a versão (o agente não passa dela).
podman container exists patchd-server || { echo "o servidor não está no ar (./deploy/atualizar.sh antes)" >&2; exit 1; }
srv="$(podman exec patchd-server /patchd-server -version | awk '{print $2}')"
if [ "$VERSAO" != "$srv" ] && [ "$(printf '%s\n%s\n' "$srv" "$VERSAO" | sort -V | tail -1)" = "$VERSAO" ]; then
  echo "o agente $VERSAO seria mais novo que o servidor ($srv): ./deploy/atualizar.sh $VERSAO antes" >&2
  exit 1
fi
JOBPUB="$(podman exec patchd-server /patchd-server job pubkey 2>/dev/null | grep -oE '^[A-Za-z0-9+/]{43}=$' || true)"
[ -n "$JOBPUB" ] || { echo "o servidor não devolveu a chave pública de jobs (podman exec patchd-server /patchd-server job pubkey)" >&2; exit 1; }

# A mesma imagem do Go do deploy/Dockerfile: o agente e o servidor saem do mesmo Go.
GOIMG="$(sed -n 's/^FROM \(docker\.io\/library\/golang:[^ ]*\).*/\1/p' deploy/Dockerfile | head -1)"
[ -n "$GOIMG" ] || { echo "não achei a imagem do Go no deploy/Dockerfile" >&2; exit 1; }

# Um clone limpo do commit atual: o build não toca no repositório do servidor.
# Em ~/.cache, e não no /run/user (um tmpfs pequeno): o build gera uns 200 MB.
mkdir -p "$HOME/.cache"
T="$(mktemp -d "$HOME/.cache/patchd-agente.XXXXXX")"
apaga_chave=false
case "$(realpath "$KEY")" in /dev/shm/*|"${XDG_RUNTIME_DIR:-/nenhum}"/*) apaga_chave=true ;; esac
fim() {
  rm -rf "$T"
  if $apaga_chave; then rm -f "$KEY"; echo "a chave de release saiu da memória ($KEY)"; fi
}
trap fim EXIT
git clone -q --no-hardlinks "$REPO" "$T/src"
mkdir -p "$T/saida" "$T/src/.docker/certs"
# CA corporativa (a mesma do build do servidor): fora do git, copiada do repositório.
cp "$REPO"/.docker/certs/*.crt "$T/src/.docker/certs/" 2>/dev/null || true

echo "== compilando e assinando o agente $VERSAO (commit $(git rev-parse --short=12 HEAD)) em $GOIMG"
# A chave entra pela entrada padrão e vai para um tmpfs do container: não toca no disco
# daqui nem fica numa camada. As caches do Go ficam em volumes, para a próxima vez.
# shellcheck disable=SC2016
podman run --rm -i \
  -v "$T/src:/src:Z" -v "$T/saida:/saida:Z" \
  -v patchd-gomod:/go/pkg/mod -v patchd-gocache:/root/.cache/go-build \
  --tmpfs /chave:mode=0700 -w /src \
  -e VERSAO="$VERSAO" -e PATCHD_JOB_PUBKEY="$JOBPUB" \
  -e PATCHD_UPDATE_PUBKEY_NEXT="$(val PATCHD_UPDATE_PUBKEY_NEXT)" \
  -e PATCHD_JOB_PUBKEY_NEXT="$(val PATCHD_JOB_PUBKEY_NEXT)" \
  -e PATCHD_APP_COMPANY="$(val PATCHD_APP_COMPANY)" \
  -e HTTPS_PROXY="${HTTPS_PROXY:-$(val HTTPS_PROXY)}" -e HTTP_PROXY="${HTTP_PROXY:-$(val HTTP_PROXY)}" \
  -e NO_PROXY="${NO_PROXY:-$(val NO_PROXY)}" \
  "$GOIMG" bash -c '
    set -euo pipefail
    umask 077; cat > /chave/release.key
    if ls .docker/certs/*.crt >/dev/null 2>&1; then
      cp .docker/certs/*.crt /usr/local/share/ca-certificates/ && update-ca-certificates >/dev/null
    fi
    git config --global --add safe.directory /src
    go build -trimpath -o /tmp/patchd-release ./cmd/release
    PATCHD_UPDATE_PUBKEY="$(/tmp/patchd-release pubkey -key /chave/release.key)"
    export PATCHD_UPDATE_PUBKEY
    ./scripts/build.sh "$VERSAO"
    /tmp/patchd-release sign -key /chave/release.key -version "$VERSAO" -dist dist -out /saida
  ' < "$KEY"

[ -s "$T/saida/$VERSAO/manifest.json" ] || { echo "a assinatura não gerou $VERSAO/manifest.json" >&2; exit 1; }
cp -r "$T/saida/$VERSAO" "$RELEASES/"
echo "== publicando $VERSAO"
publicar "$VERSAO"
$apaga_chave || echo "AVISO: a chave de release continua em $KEY, gravada neste servidor; o recomendado é /dev/shm (sai no fim)" >&2
echo "pronto: Configuração › Agente mostra a $VERSAO para baixar; os agentes já instalados a recebem no próximo check-in"
