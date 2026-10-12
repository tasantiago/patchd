#!/usr/bin/env bash
# docker compose do patchd em produção, sem as PATCHD_* do shell (Aula 10.4):
#   ./deploy/compose.sh ps
#   ./deploy/compose.sh logs --tail 50 server
#   ./deploy/compose.sh up -d          (sobe a versão anotada em deploy/.env)
# Para trocar de versão, use ./deploy/atualizar.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
. deploy/comum.sh
dc "$@"
