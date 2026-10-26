#!/usr/bin/env bash
# docker compose do patchd em produção, sem as PATCHD_* do shell (Aula 10.4):
#   ./deploy/compose.sh ps
#   ./deploy/compose.sh logs --tail 50 server
#   ./deploy/compose.sh up -d          (sobe a versão anotada em deploy/.env)
# Para trocar de versão, use ./deploy/atualizar.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
. deploy/comum.sh
if [ "$MOTOR" = podman ]; then
  cat >&2 <<'MSG'
este host usa o Podman (PATCHD_MOTOR=podman no deploy/.env): não há compose. O equivalente:
  systemctl --user status patchd-server patchd-postgres     estado (e o healthcheck)
  journalctl --user -u patchd-server -n 50                  log do servidor
  podman exec patchd-server /patchd-server ARGS...          linha de comando do servidor
  systemctl --user restart patchd-server                    reinício (espera ficar saudável)
  ./deploy/atualizar.sh vX.Y.Z                              troca de versão, como no compose
MSG
  exit 2
fi
dc "$@"
