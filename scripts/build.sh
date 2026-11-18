#!/usr/bin/env bash
# Compila o patchd-agent para os seis alvos e o patchd-server para Linux, com a versão injetada.
# Uso (dentro do container): ./scripts/build.sh [versão]    exemplo: ./scripts/build.sh v0.1.0
# Versões sem o sufixo "-dev" são builds de release e exigem a árvore do git limpa.
set -euo pipefail

# Sem Go, o script para aqui, antes de tocar no dist/ (um binário antigo não pode parecer resultado novo).
if ! command -v go >/dev/null 2>&1; then
  echo "ERRO: go não encontrado. Rode dentro do container: docker compose exec dev ./scripts/build.sh $*" >&2
  exit 1
fi

cd "$(git rev-parse --show-toplevel)"

# Versão: argumento, ou a descrição do git (tag, hash, "-dirty" se houver alterações), ou "dev".
VERSAO="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# Trava de release: binário de release precisa corresponder exatamente a um commit.
if [ "$#" -ge 1 ] && [[ "$VERSAO" != *-dev ]] && [ -n "$(git status --porcelain)" ]; then
  echo "ERRO: build de release ($VERSAO) com alterações não commitadas." >&2
  echo "Faça commit ou use uma versão terminada em -dev." >&2
  exit 1
fi

MODULO="$(go list -m)"

# -s -w: remove tabela de símbolos e dados de depuração (binário menor; o stack trace de panic continua).
# -X: injeta a versão na variável buildinfo.Version.
LDFLAGS="-s -w -X ${MODULO}/internal/buildinfo.Version=${VERSAO}"

# Chave pública de atualização (Aula 5.4): só por variável, nunca no repositório. Sem ela,
# os agentes deste build não se atualizam sozinhos; um build de release exige a chave.
if [ -n "${PATCHD_UPDATE_PUBKEY:-}" ]; then
  if ! [[ "$PATCHD_UPDATE_PUBKEY" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
    echo "ERRO: PATCHD_UPDATE_PUBKEY não parece uma chave do patchd-release keygen (base64 de 32 bytes)." >&2
    exit 1
  fi
  LDFLAGS="${LDFLAGS} -X ${MODULO}/internal/release.PublicKey=${PATCHD_UPDATE_PUBKEY}"
  echo "chave de atualização embutida nos agentes"
elif [[ "$VERSAO" != *-dev ]]; then
  echo "ERRO: build de release ($VERSAO) sem PATCHD_UPDATE_PUBKEY: os agentes não conseguiriam se atualizar." >&2
  exit 1
else
  echo "aviso: sem PATCHD_UPDATE_PUBKEY; os agentes deste build não se atualizam sozinhos" >&2
fi

# Chave pública dos jobs (Aula 8.1): o agente só executa jobs assinados pela chave
# privada correspondente, que fica no servidor (patchd-server job keygen). Sem ela, os
# agentes deste build recusam todo job; um build de release exige a chave.
if [ -n "${PATCHD_JOB_PUBKEY:-}" ]; then
  if ! [[ "$PATCHD_JOB_PUBKEY" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
    echo "ERRO: PATCHD_JOB_PUBKEY não parece uma chave do patchd-server job keygen (base64 de 32 bytes)." >&2
    exit 1
  fi
  if [ "${PATCHD_JOB_PUBKEY}" = "${PATCHD_UPDATE_PUBKEY:-}" ]; then
    echo "ERRO: PATCHD_JOB_PUBKEY igual à PATCHD_UPDATE_PUBKEY: as duas chaves têm de ser diferentes." >&2
    exit 1
  fi
  LDFLAGS="${LDFLAGS} -X ${MODULO}/internal/jobs.PublicKey=${PATCHD_JOB_PUBKEY}"
  echo "chave de jobs embutida nos agentes"
elif [[ "$VERSAO" != *-dev ]]; then
  echo "ERRO: build de release ($VERSAO) sem PATCHD_JOB_PUBKEY: os agentes recusariam todos os jobs." >&2
  exit 1
else
  echo "aviso: sem PATCHD_JOB_PUBKEY; os agentes deste build recusam todos os jobs" >&2
fi

# Chaves seguintes (Aula 20.7, S8): aceitas pelos agentes junto com as atuais, para a troca
# de chave sem parar a frota (roteiro em docs/operacao.md). Opcionais; cada uma tem de ser
# diferente das outras três (uma "seguinte" igual à atual não protege nada).
for par in "PATCHD_UPDATE_PUBKEY_NEXT:release.NextPublicKey:atualização" "PATCHD_JOB_PUBKEY_NEXT:jobs.NextPublicKey:jobs"; do
  var="${par%%:*}"; resto="${par#*:}"; alvo="${resto%%:*}"; nome="${resto#*:}"
  valor="${!var:-}"
  [ -n "$valor" ] || continue
  if ! [[ "$valor" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
    echo "ERRO: $var não parece uma chave pública do patchd (base64 de 32 bytes)." >&2
    exit 1
  fi
  for outra in PATCHD_UPDATE_PUBKEY PATCHD_JOB_PUBKEY PATCHD_UPDATE_PUBKEY_NEXT PATCHD_JOB_PUBKEY_NEXT; do
    if [ "$outra" != "$var" ] && [ "$valor" = "${!outra:-}" ]; then
      echo "ERRO: $var igual à $outra: as chaves têm de ser todas diferentes." >&2
      exit 1
    fi
  done
  LDFLAGS="${LDFLAGS} -X ${MODULO}/internal/${alvo}=${valor}"
  echo "chave seguinte de ${nome} embutida nos agentes"
done

# Recurso de versão do Windows (Aula 20.9): a "Descrição do arquivo" é o nome que o Windows
# mostra no Gerenciador de Tarefas, nos aplicativos de inicialização e no título dos avisos da
# Central. O agente se chama "Agente patchd"; a Central (cmd/app), o nome do painel
# (PATCHD_APP_NAME, que o deploy/agente.sh lê do servidor no ar). PATCHD_APP_COMPANY é a
# organização (opcional). Os .syso e a Central embutida são gerados aqui, fora do git, e
# apagados no fim. A Central leva o ícone (cmd/app/icone): o do Explorer, do atalho na área de
# trabalho e o perto do relógio; PATCHD_APP_ICON troca por outro .ico.
APP_NAME="${PATCHD_APP_NAME:-Central de software}"
APP_ICON="${PATCHD_APP_ICON:-cmd/app/icone/central-de-software.ico}"
limpa_gerados() {
  rm -f cmd/agent/versao_windows_*.syso cmd/app/versao_windows_*.syso cmd/agent/patchd-app.exe.gz cmd/agent/patchd-app.exe.sha256
}
trap limpa_gerados EXIT
go run ./cmd/winres -version "$VERSAO" -name "Agente patchd" -company "${PATCHD_APP_COMPANY:-}" -out cmd/agent > /dev/null
go run ./cmd/winres -version "$VERSAO" -name "$APP_NAME" -file patchd-app.exe -company "${PATCHD_APP_COMPANY:-}" -icon "$APP_ICON" -out cmd/app > /dev/null
echo "recurso de versão do Windows gravado (Central: \"$APP_NAME\", ícone $APP_ICON)"

ALVOS_AGENTE="windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

rm -rf dist
mkdir -p dist

# A Central do Windows (cmd/app) é um executável próprio, compilado como programa gráfico
# (-H windowsgui: aberta no logon, sem janela preta). O agente do Windows a leva embutida,
# comprimida, com o SHA-256, e a grava sem mudar um byte: nada de executável montado na
# máquina, que um antivírus por aprendizado de máquina toma por "dropper". A cópia em dist/
# serve para conferir o hash (SHA256SUMS) e para pedir a exceção ao antivírus.
LDFLAGS_APP="-s -w -X ${MODULO}/internal/buildinfo.Version=${VERSAO} -H windowsgui"

for alvo in $ALVOS_AGENTE; do
  os="${alvo%/*}"
  arch="${alvo#*/}"
  ext=""
  tags=""
  if [ "$os" = "windows" ]; then
    ext=".exe"
    tags="-tags patchdapp"
    app="dist/patchd-app-windows-${arch}.exe"
    GOOS=windows GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS_APP" -o "$app" ./cmd/app
    gzip -9 -n -c "$app" > cmd/agent/patchd-app.exe.gz
    sha256sum "$app" | cut -d' ' -f1 > cmd/agent/patchd-app.exe.sha256
    echo "ok  $app"
  fi
  saida="dist/patchd-agent-${os}-${arch}${ext}"
  # -trimpath: remove os caminhos da máquina de build do binário.
  # shellcheck disable=SC2086
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath $tags -ldflags "$LDFLAGS" -o "$saida" ./cmd/agent
  rm -f cmd/agent/patchd-app.exe.gz cmd/agent/patchd-app.exe.sha256
  echo "ok  $saida"
done

# PATCHD_BUILD_SERVER=0: só o agente (o deploy/agente.sh: o servidor sai da imagem do
# atualizar.sh, e os dois binários dele custariam uns 80 MB no disco do build).
if [ "${PATCHD_BUILD_SERVER:-1}" != 0 ]; then
  for arch in amd64 arm64; do
    saida="dist/patchd-server-linux-${arch}"
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$saida" ./cmd/server
    echo "ok  $saida"
  done
fi

# O script de instalação da frota Windows vai junto: dist/ vira o pacote do ITOM e da GPO.
cp deploy/agent/windows/instalar.cmd dist/
echo "ok  dist/instalar.cmd"

# Hashes para conferência ao copiar os binários para as máquinas de teste.
(cd dist && sha256sum -- * > SHA256SUMS)
echo "versão: $VERSAO"
