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

ALVOS_AGENTE="windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

rm -rf dist
mkdir -p dist

for alvo in $ALVOS_AGENTE; do
  os="${alvo%/*}"
  arch="${alvo#*/}"
  ext=""
  if [ "$os" = "windows" ]; then ext=".exe"; fi
  saida="dist/patchd-agent-${os}-${arch}${ext}"
  # -trimpath: remove os caminhos da máquina de build do binário.
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$saida" ./cmd/agent
  echo "ok  $saida"
done

for arch in amd64 arm64; do
  saida="dist/patchd-server-linux-${arch}"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$saida" ./cmd/server
  echo "ok  $saida"
done

# O script de instalação da frota Windows vai junto: dist/ vira o pacote do ITOM e da GPO.
cp deploy/agent/windows/instalar.cmd dist/
echo "ok  dist/instalar.cmd"

# Hashes para conferência ao copiar os binários para as máquinas de teste.
(cd dist && sha256sum -- * > SHA256SUMS)
echo "versão: $VERSAO"
