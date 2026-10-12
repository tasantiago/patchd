#!/usr/bin/env bash
# Cópia do banco do compose de produção (Aula 10.1).
#
#   ./deploy/backup.sh [PASTA]
#
# pg_dump no formato custom (-Fc: comprimido, restaurável tabela a tabela) de dentro do
# container do Postgres, sem parar o servidor: o pg_dump lê uma foto consistente do banco.
# A cópia é conferida (pg_restore --list lê o arquivo inteiro) antes de ganhar o nome final,
# recebe um .sha256 ao lado e as contagens do momento; cópias mais velhas que
# PATCHD_BACKUP_KEEP_DAYS (padrão 14) são apagadas. Duas execuções ao mesmo tempo: a
# segunda espera.
#
# A cópia tem tudo o que está no banco (hashes de senha, de credenciais, a fila de jobs):
# a pasta é 700 e os arquivos 600. Leve as cópias para fora deste host (pendência: cifrar).
# O que NÃO está no banco e também precisa de cópia, à parte: deploy/secrets, deploy/tls,
# deploy/.env e, offline, a chave privada de release (docs/operacao.md).
set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE="${PATCHD_COMPOSE_FILE:-deploy/compose.yaml}"
DEST="${1:-${PATCHD_BACKUP_DIR:-deploy/backups}}"
KEEP_DAYS="${PATCHD_BACKUP_KEEP_DAYS:-14}"
case "$KEEP_DAYS" in ''|*[!0-9]*) echo "PATCHD_BACKUP_KEEP_DAYS deve ser um número de dias" >&2; exit 2 ;; esac

pg() { docker compose -f "$COMPOSE" exec -T postgres "$@"; }

umask 077
mkdir -p "$DEST"
chmod 700 "$DEST"
exec 9>"$DEST/.lock"
flock 9

ts="$(date -u +%Y%m%dT%H%M%SZ)"
name="patchd-$ts.dump"
tmp="$DEST/.$name.parcial"
trap 'rm -f "$tmp"' EXIT

# O pg_dump grava num arquivo dentro do container (e não num pipe): assim o índice da cópia
# leva a posição de cada tabela, e a restauração não depende da ordem de leitura.
pg pg_dump -U patchd -d patchd --format=custom --no-owner -f /tmp/patchd-backup.dump
pg cat /tmp/patchd-backup.dump > "$tmp"
pg rm -f /tmp/patchd-backup.dump
# Conferência: um arquivo cortado (disco cheio, container reiniciado) não passa daqui.
pg pg_restore --list < "$tmp" > /dev/null
mv "$tmp" "$DEST/$name"
trap - EXIT
(cd "$DEST" && sha256sum "$name" > "$name.sha256")
# Contagens no momento da cópia, para conferir uma restauração (restaurar.sh --testar).
pg psql -U patchd -d patchd -AtX -f - < deploy/contagens.sql > "$DEST/$name.contagens"

removed=0
while IFS= read -r -d '' old; do
  rm -f "$old" "$old.sha256" "$old.contagens"
  removed=$((removed + 1))
done < <(find "$DEST" -maxdepth 1 -name 'patchd-*.dump' -mtime +"$KEEP_DAYS" -print0)

size="$(du -h "$DEST/$name" | cut -f1)"
echo "cópia $DEST/$name ($size) conferida; $removed cópia(s) com mais de $KEEP_DAYS dias removida(s)"
