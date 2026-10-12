#!/usr/bin/env bash
# Cópia do banco do compose de produção (Aulas 10.1 e 10.3).
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
# A cópia tem tudo o que está no banco (hashes de senha, de credenciais, a fila de jobs).
# Com deploy/secrets/backup_recipients (chaves PÚBLICAS do age, uma por linha), a cópia sai
# cifrada (patchd-*.dump.age): a conferência roda dentro do container, antes da cifra, e o
# texto claro nunca é gravado neste host. A chave privada fica fora do servidor; só quem a
# tem restaura (restaurar.sh). Sem o arquivo, a cópia sai sem cifra, com aviso. Em todo
# caso a pasta é 700 e os arquivos 600. No fim, a cópia é registrada no banco (tabela
# backup_runs): o servidor publica a mais recente nas métricas (Aula 10.2).
# O que NÃO está no banco e também precisa de cópia, à parte: deploy/secrets, deploy/tls,
# deploy/.env e, offline, a chave privada de release (docs/operacao.md).
set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE="${PATCHD_COMPOSE_FILE:-deploy/compose.yaml}"
DEST="${1:-${PATCHD_BACKUP_DIR:-deploy/backups}}"
RECIPIENTS="${PATCHD_BACKUP_RECIPIENTS_FILE:-deploy/secrets/backup_recipients}"
KEEP_DAYS="${PATCHD_BACKUP_KEEP_DAYS:-14}"
case "$KEEP_DAYS" in ''|*[!0-9]*) echo "PATCHD_BACKUP_KEEP_DAYS deve ser um número de dias" >&2; exit 2 ;; esac

pg() { docker compose -f "$COMPOSE" exec -T postgres "$@"; }

# Cifra: decidida e conferida antes do pg_dump (destinatário inválido falha já, sem cópia).
encrypt=false
if [ -s "$RECIPIENTS" ]; then
  if grep -q 'AGE-SECRET-KEY-' "$RECIPIENTS"; then
    echo "$RECIPIENTS tem uma chave PRIVADA do age: ela não fica no servidor. Deixe só a linha age1… (pública) e guarde a privada fora daqui." >&2
    exit 1
  fi
  command -v age >/dev/null || { echo "$RECIPIENTS existe, mas o age não está instalado (Ubuntu/Debian: sudo apt install age)" >&2; exit 1; }
  : | age -R "$RECIPIENTS" > /dev/null || { echo "$RECIPIENTS: destinatário inválido (use as linhas age1… do age-keygen -y)" >&2; exit 1; }
  encrypt=true
  nrec="$(grep -cvE '^[[:space:]]*(#|$)' "$RECIPIENTS")"
else
  echo "aviso: cópia SEM cifra (falta $RECIPIENTS; docs/operacao.md, \"Cópias cifradas\")" >&2
fi

umask 077
mkdir -p "$DEST"
chmod 700 "$DEST"
exec 9>"$DEST/.lock"
flock 9

ts="$(date -u +%Y%m%dT%H%M%SZ)"
name="patchd-$ts.dump"
$encrypt && name="$name.age"
tmp="$DEST/.$name.parcial"
trap 'rm -f "$tmp"; pg rm -f /tmp/patchd-backup.dump' EXIT

# O pg_dump grava num arquivo dentro do container (e não num pipe): assim o índice da cópia
# leva a posição de cada tabela, e a restauração não depende da ordem de leitura.
pg pg_dump -U patchd -d patchd --format=custom --no-owner -f /tmp/patchd-backup.dump
# Conferência: um arquivo cortado (disco cheio, container reiniciado) não passa daqui.
pg pg_restore --list /tmp/patchd-backup.dump > /dev/null
if $encrypt; then
  # Cifrada no caminho: o texto claro sai do container direto para o age.
  pg cat /tmp/patchd-backup.dump | age -R "$RECIPIENTS" > "$tmp"
else
  pg cat /tmp/patchd-backup.dump > "$tmp"
  pg pg_restore --list < "$tmp" > /dev/null
fi
pg rm -f /tmp/patchd-backup.dump
mv "$tmp" "$DEST/$name"
trap - EXIT
(cd "$DEST" && sha256sum "$name" > "$name.sha256")
# Contagens no momento da cópia, para conferir uma restauração (restaurar.sh --testar).
pg psql -U patchd -d patchd -AtX -f - < deploy/contagens.sql > "$DEST/$name.contagens"
# Registro no banco, para as métricas (Aula 10.3). Falhar aqui não desfaz a cópia.
bytes="$(stat -c %s "$DEST/$name")"
if ! pg psql -U patchd -d patchd -v ON_ERROR_STOP=1 -qAtX -v arq="$name" -v bytes="$bytes" -v cif="$encrypt" > /dev/null <<'SQL'
INSERT INTO backup_runs (file, bytes, encrypted) VALUES (:'arq', :bytes, :cif);
DELETE FROM backup_runs WHERE finished_at < now() - interval '400 days';
SQL
then
  echo "aviso: a cópia não foi registrada no banco (o servidor já aplicou a migration 0023?)" >&2
fi

removed=0
while IFS= read -r -d '' old; do
  rm -f "$old" "$old.sha256" "$old.contagens"
  removed=$((removed + 1))
done < <(find "$DEST" -maxdepth 1 \( -name 'patchd-*.dump' -o -name 'patchd-*.dump.age' \) -mtime +"$KEEP_DAYS" -print0)

size="$(du -h "$DEST/$name" | cut -f1)"
if $encrypt; then cifra="cifrada para $nrec destinatário(s)"; else cifra="SEM cifra"; fi
echo "cópia $DEST/$name ($size, $cifra) conferida; $removed cópia(s) com mais de $KEEP_DAYS dias removida(s)"
