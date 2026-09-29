#!/usr/bin/env bash
# Compila o patchd-agent para os seis alvos e o patchd-server para Linux, com a versão injetada.
# Uso (dentro do container): ./scripts/build.sh [versão]    exemplo: ./scripts/build.sh v0.1.0
# Versões sem o sufixo "-dev" são builds de release e exigem a árvore do git limpa.
set -euo pipefail

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

# Hashes para conferência ao copiar os binários para as máquinas de teste.
(cd dist && sha256sum -- * > SHA256SUMS)
echo "versão: $VERSAO"
