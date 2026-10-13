#!/usr/bin/env bash
# Atualização do patchd-server em produção, com volta (Aula 10.4).
#
#   ./deploy/atualizar.sh VERSAO              constrói a imagem do commit atual, faz uma
#                                             cópia do banco, troca o servidor e confere
#   ./deploy/atualizar.sh --voltar            volta para a versão anterior, se o banco não
#                                             mudou (só a imagem volta)
#   ./deploy/atualizar.sh --voltar --confirmo volta também o banco, restaurando a cópia de
#                                             antes da atualização (perde o que veio depois)
#
# A versão em uso fica anotada em deploy/.env (PATCHD_VERSION e PATCHD_COMMIT): qualquer
# "./deploy/compose.sh up -d" sobe a mesma imagem. A última atualização fica descrita em
# deploy/.atualizacao (fora do git), que o --voltar lê.
#
# Por que a volta às vezes precisa da cópia: as migrations só andam para a frente, e o
# servidor antigo recusa um banco com migration que ele não conhece. Nada aqui volta
# sozinho: se a troca falhar, o script para e diz o comando da volta.
set -euo pipefail
cd "$(dirname "$0")/.."
. deploy/comum.sh

ESTADO=deploy/.atualizacao
uso() { sed -n '4,10p' "$0" | sed 's/^# //' >&2; exit 2; }
versao_ok() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; }
estado() { sed -n "s/^$1=//p" "$ESTADO" | tail -1; }
migration() { pg psql -U patchd -d patchd -AtXc 'SELECT coalesce(max(version), 0) FROM schema_migrations'; }
# O Postgres precisa estar no ar para a cópia e para ler a migration, mesmo com o compose
# todo parado (Aula 10.5b: a atualização parava em silêncio no "service postgres is not
# running"). Subir só o banco não muda nada no servidor.
banco_no_ar() {
  dc up -d --wait postgres >/dev/null 2>&1 ||
    { echo "o Postgres do compose não subiu: ./deploy/compose.sh logs --tail 30 postgres" >&2; exit 1; }
}
imagem_existe() { docker image inspect "patchd-server:$1" >/dev/null 2>&1; }

# rotulo IMAGEM_OU_CONTAINER CHAVE: um rótulo OCI (vazio se não houver; imagens de antes
# da Aula 10.4 não têm).
rotulo() {
  local v
  v="$(docker inspect -f "{{index .Config.Labels \"org.opencontainers.image.$2\"}}" "$1" 2>/dev/null || true)"
  [ "$v" = "<no value>" ] && v=""
  echo "$v"
}

# A versão e o commit em uso: os rótulos da imagem do container; sem rótulo, a tag da
# imagem (patchd-server:VERSAO) e o commit anotado no .env.
em_uso() {
  local id v="" c=""
  id="$(dc ps -q server 2>/dev/null || true)"
  if [ -n "$id" ]; then
    v="$(rotulo "$id" version)"; c="$(rotulo "$id" revision)"
    [ -n "$v" ] || v="$(docker inspect -f '{{.Config.Image}}' "$id" 2>/dev/null | sed -n 's/^patchd-server://p')"
  fi
  [ -n "$v" ] || v="$(env_get PATCHD_VERSION)"
  [ -n "$c" ] || c="$(env_get PATCHD_COMMIT)"
  echo "${v:-dev} ${c}"
}

subir() { # VERSAO COMMIT: troca o servidor e espera ficar saudável
  env_set PATCHD_VERSION "$1"
  env_set PATCHD_COMMIT "$2"
  dc up -d --wait --wait-timeout 180 server
}

[ -s deploy/.env ] || { echo "falta deploy/.env" >&2; exit 1; }

if [ "${1:-}" = "--voltar" ]; then
  confirmo=false
  case "${2:-}" in --confirmo) confirmo=true ;; "") ;; *) uso ;; esac
  [ -s "$ESTADO" ] || { echo "nenhuma atualização anotada em $ESTADO" >&2; exit 1; }
  [ -z "$(estado DESFEITA)" ] || { echo "a última atualização já foi desfeita em $(estado DESFEITA)" >&2; exit 1; }
  ant="$(estado VERSAO_ANTERIOR)"; ant_commit="$(estado COMMIT_ANTERIOR)"
  nova="$(estado VERSAO_NOVA)"; copia="$(estado COPIA)"; mig_antes="$(estado MIGRATION_ANTES)"
  imagem_existe "$ant" || { echo "a imagem patchd-server:$ant não existe mais neste host; construa-a do commit ${ant_commit:-anterior} (git checkout e ./deploy/compose.sh build) antes de voltar" >&2; exit 1; }
  banco_no_ar
  mig_agora="$(migration)" || { echo "não consegui ler a versão do banco" >&2; exit 1; }
  echo "voltando de $nova para $ant (banco: migration $mig_agora; antes da atualização: $mig_antes)"
  if [ "$mig_agora" = "$mig_antes" ]; then
    subir "$ant" "$ant_commit"
    echo "voltou: o banco não tinha mudado, só a imagem foi trocada"
  else
    if ! $confirmo; then
      cat >&2 <<MSG
o banco mudou com a atualização (migration $mig_antes -> $mig_agora): o servidor $ant não sobe nele.
Voltar exige restaurar a cópia de antes ($copia), e tudo o que aconteceu depois dela se perde
(check-ins, buscas, jobs). Para seguir: ./deploy/atualizar.sh --voltar --confirmo
MSG
      exit 1
    fi
    [ -s "$copia" ] || { echo "a cópia de antes não está mais em $copia" >&2; exit 1; }
    case "$copia" in
      *.age) [ -r "${PATCHD_BACKUP_IDENTITY_FILE:-}" ] || { echo "a cópia é cifrada: defina PATCHD_BACKUP_IDENTITY_FILE com a chave privada" >&2; exit 1; } ;;
    esac
    # A versão anterior vai para o .env antes: o restaurar.sh sobe o servidor no fim.
    env_set PATCHD_VERSION "$ant"
    env_set PATCHD_COMMIT "$ant_commit"
    ./deploy/restaurar.sh --confirmo "$copia"
    dc up -d --wait --wait-timeout 180 server
    echo "voltou: banco restaurado da cópia de antes da atualização e servidor $ant no ar"
  fi
  printf 'DESFEITA=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$ESTADO"
  exit 0
fi

[ $# -eq 1 ] || uso
nova="$1"
versao_ok "$nova" || { echo "versão inválida: $nova (ex.: v0.10.4)" >&2; exit 2; }
# A imagem leva o commit no rótulo e no binário: com alterações não commitadas, ela não
# corresponderia a commit nenhum.
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "há alterações não commitadas: commit antes de atualizar" >&2; exit 1; }
commit="$(git rev-parse HEAD)"
read -r ant ant_commit <<< "$(em_uso)"
[ "$ant" != "$nova" ] || { echo "o servidor já está em $nova" >&2; exit 1; }
# A mesma versão de outro commit confundiria a volta: uma tag, um commit.
if imagem_existe "$nova" && [ "$(rotulo "patchd-server:$nova" revision)" != "$commit" ]; then
  echo "a imagem patchd-server:$nova já existe e é de outro commit; use outra versão" >&2
  exit 1
fi

banco_no_ar
echo "== 1/4 construindo patchd-server:$nova (commit ${commit:0:12}); o servidor atual continua no ar"
dc_versao "$nova" "$commit" build server
got="$(docker run --rm "patchd-server:$nova" -version)"
case "$got" in *"$nova"*) ;; *) echo "a imagem nova não diz a versão esperada: $got" >&2; exit 1 ;; esac

echo "== 2/4 cópia do banco antes da troca"
mig_antes="$(migration)" || { echo "não consegui ler a versão do banco" >&2; exit 1; }
./deploy/backup.sh
copia="$(ls -t "${PATCHD_BACKUP_DIR:-deploy/backups}"/patchd-*.dump* 2>/dev/null | grep -E '\.dump(\.age)?$' | head -1)"

umask 077
cat > "$ESTADO" <<EST
QUANDO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
VERSAO_ANTERIOR=$ant
COMMIT_ANTERIOR=$ant_commit
VERSAO_NOVA=$nova
COMMIT_NOVO=$commit
MIGRATION_ANTES=$mig_antes
COPIA=$copia
EST

echo "== 3/4 trocando o servidor: $ant -> $nova"
if ! subir "$nova" "$commit"; then
  echo "o servidor $nova não ficou saudável. Últimas linhas do log:" >&2
  dc logs --tail 20 server >&2 || true
  echo "Para voltar: ./deploy/atualizar.sh --voltar (e --confirmo, se ele pedir)" >&2
  exit 1
fi

echo "== 4/4 conferência"
mig_depois="$(migration)"
printf 'MIGRATION_DEPOIS=%s\n' "$mig_depois" >> "$ESTADO"
echo "patchd-server $ant -> $nova no ar e saudável; banco: migration $mig_antes -> $mig_depois"
echo "cópia de antes da troca: $copia"
if [ "$mig_antes" = "$mig_depois" ]; then
  echo "o banco não mudou: ./deploy/atualizar.sh --voltar troca só a imagem"
else
  echo "o banco mudou: voltar exige restaurar essa cópia (./deploy/atualizar.sh --voltar --confirmo)"
fi
