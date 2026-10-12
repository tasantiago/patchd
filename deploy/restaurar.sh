#!/usr/bin/env bash
# Restauração do banco do compose de produção a partir de uma cópia do backup.sh (Aulas 10.1 e 10.3).
#
#   ./deploy/restaurar.sh --testar  ARQUIVO.dump[.age]   restaura num banco temporário, compara
#                                                        as contagens com as da cópia e apaga o banco
#   ./deploy/restaurar.sh --confirmo ARQUIVO.dump[.age]  SUBSTITUI o banco patchd pela cópia
#
# Cópia cifrada (.age): PATCHD_BACKUP_IDENTITY_FILE=caminho da chave PRIVADA do age (a que
# fica fora do servidor; se ela tiver senha, o age pergunta). O texto claro vai do age direto
# para dentro do container do Postgres, sem passar pelo disco deste host.
#
# O teste não toca no banco em uso: é o ensaio que deve rodar todo mês (uma cópia que nunca
# foi restaurada não é uma cópia). A substituição para o servidor, apaga o banco patchd,
# cria de novo e restaura em uma transação só (se algo falhar no meio, o banco fica vazio,
# não pela metade: rode de novo), e sobe o servidor. Apagar e criar, e não restaurar por
# cima: tabelas de migrations mais novas que a cópia não podem sobrar.
set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE="${PATCHD_COMPOSE_FILE:-deploy/compose.yaml}"
uso() { sed -n '4,7p' "$0" | sed 's/^# //' >&2; exit 2; }
[ $# -eq 2 ] || uso
mode="$1"; dump="$2"
case "$mode" in --testar|--confirmo) ;; *) uso ;; esac
[ -s "$dump" ] || { echo "cópia não encontrada ou vazia: $dump" >&2; exit 1; }
cifrada=false
case "$dump" in
  *.age)
    cifrada=true
    id="${PATCHD_BACKUP_IDENTITY_FILE:-}"
    [ -n "$id" ] && [ -r "$id" ] || { echo "cópia cifrada: defina PATCHD_BACKUP_IDENTITY_FILE com a chave privada do age (fora do servidor)" >&2; exit 1; }
    command -v age >/dev/null || { echo "o age não está instalado (Ubuntu/Debian: sudo apt install age)" >&2; exit 1; }
    ;;
esac

dc() { docker compose -f "$COMPOSE" "$@"; }
pg() { dc exec -T postgres "$@"; }
psqlc() { pg psql -U patchd -d "$1" -v ON_ERROR_STOP=1 -AtX -c "SET client_min_messages TO warning" -c "$2"; }
tabela() { if command -v column >/dev/null; then column -t -s '|'; else cat; fi; }

# 1. Integridade: o SHA-256 gravado pelo backup.sh, se estiver ao lado.
if [ -f "$dump.sha256" ]; then
  (cd "$(dirname "$dump")" && sha256sum --quiet -c "$(basename "$dump").sha256") || { echo "SHA-256 não confere: cópia corrompida" >&2; exit 1; }
  echo "SHA-256 confere"
else
  echo "aviso: sem $dump.sha256; seguindo sem a conferência do SHA-256" >&2
fi

dc up -d --wait postgres >/dev/null
# A cópia vai para dentro do container (arquivo, não pipe: o pg_restore pode pular no índice).
limpa() { pg rm -f /tmp/patchd-restaurar.dump; }
trap limpa EXIT
if $cifrada; then
  # Chave errada ou arquivo adulterado: o age para com erro (cada bloco é autenticado).
  age -d -i "$id" "$dump" | pg sh -c 'cat > /tmp/patchd-restaurar.dump' ||
    { echo "não consegui decifrar $dump (chave privada de outro par, ou arquivo adulterado)" >&2; exit 1; }
  echo "decifrada"
else
  pg sh -c 'cat > /tmp/patchd-restaurar.dump' < "$dump"
fi
# Legível antes de qualquer DROP: com --confirmo, uma cópia ruim não apaga o banco em uso.
pg pg_restore --list /tmp/patchd-restaurar.dump > /dev/null || { echo "a cópia não é um pg_dump legível" >&2; exit 1; }

if [ "$mode" = "--testar" ]; then
  db=patchd_teste_restauracao
  psqlc postgres "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
  psqlc postgres "CREATE DATABASE $db OWNER patchd" >/dev/null
  trap 'psqlc postgres "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null; limpa' EXIT
  pg pg_restore -U patchd -d "$db" --no-owner --exit-on-error --single-transaction /tmp/patchd-restaurar.dump
  got="$(pg psql -U patchd -d "$db" -AtX -f - < deploy/contagens.sql)"
  echo "== contagens restauradas"
  echo "$got" | tabela
  if [ -f "$dump.contagens" ]; then
    if [ "$got" = "$(cat "$dump.contagens")" ]; then
      echo "confere com as contagens do momento da cópia"
    else
      echo "DIFERENTE das contagens do momento da cópia:" >&2
      diff <(cat "$dump.contagens") <(echo "$got") >&2 || true
      exit 1
    fi
  fi
  echo "teste concluído; o banco temporário foi apagado e o banco em uso não mudou"
  exit 0
fi

# --confirmo: substitui o banco em uso.
echo "== parando o servidor"
dc stop server >/dev/null
echo "== apagando e recriando o banco patchd"
psqlc postgres "DROP DATABASE IF EXISTS patchd WITH (FORCE)" >/dev/null
psqlc postgres "CREATE DATABASE patchd OWNER patchd" >/dev/null
echo "== restaurando $(basename "$dump")"
pg pg_restore -U patchd -d patchd --no-owner --exit-on-error --single-transaction /tmp/patchd-restaurar.dump
pg psql -U patchd -d patchd -AtX -f - < deploy/contagens.sql | tabela
echo "== subindo o servidor (ele aplica as migrations mais novas que a cópia, se houver)"
dc up -d server >/dev/null
echo "restauração concluída; confira o log: docker compose -f $COMPOSE logs server | tail"
