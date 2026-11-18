#!/usr/bin/env bash
# Libera espaço no disco do servidor (Podman): o que as atualizações e os builds do agente
# deixam para trás e nada mais apaga.
#
#   ./deploy/limpar.sh             mostra o que ocupa e o que sairia (não apaga nada)
#   ./deploy/limpar.sh --aplicar   apaga
#
# O que sai:
#   - restos de builds do agente interrompidos (~/.cache/patchd-agente.*, com mais de 1 hora);
#   - as camadas de compilação sem nome do Podman (o estágio do Go de cada atualizar.sh);
#   - as imagens patchd-server de versões antigas, menos a em uso e a anterior (a do
#     ./deploy/atualizar.sh --voltar);
#   - a cache de compilação do Go dos builds do agente (o volume patchd-gocache; o próximo
#     build do agente demora mais, e a recria);
#   - as versões do agente antigas: ficam a publicada e as duas mais novas além dela.
#
# O que NÃO sai: o banco, as cópias do banco (o backup.sh já apaga as de mais de
# PATCHD_BACKUP_KEEP_DAYS dias), o conteúdo (catálogo e pacotes), a imagem do Go e a cache de
# módulos (patchd-gomod), que o próximo build usaria.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="$(pwd)"
. deploy/comum.sh

aplicar=false
case "${1:-}" in
  --aplicar) aplicar=true ;;
  "") ;;
  *) sed -n '5,6p' "$0" | sed 's/^# //' >&2; exit 2 ;;
esac
[ "$MOTOR" = podman ] || { echo "PATCHD_MOTOR=$MOTOR: este script é do servidor no Podman" >&2; exit 1; }
val() { env_get "$1" | tr -d '\r"'"'"; }

rel="$(val PATCHD_RELEASES_HOST_DIR)"; rel="${rel:-../releases}"
case "$rel" in /*) ;; *) rel="$REPO/deploy/$rel" ;; esac
RELEASES="$(realpath -m "$rel")"
BACKUPS="$(val PATCHD_BACKUP_DIR)"; BACKUPS="${BACKUPS:-deploy/backups}"
GRAPH="$(podman info --format '{{.Store.GraphRoot}}' 2>/dev/null || true)"

tam() { { du -sh "$@" 2>/dev/null || true; } | awk 'NR==1 {print $1}'; }
faz() {
  if $aplicar; then "$@"; else printf '    (sairia) %s\n' "$*"; fi
}

echo "== espaço"
df -h "$REPO" "$HOME" ${GRAPH:+"$GRAPH"} "$RELEASES" 2>/dev/null | awk 'NR==1 || !seen[$1]++'
echo
echo "== o que ocupa"
printf '  %-46s %s\n' "armazenamento do Podman (imagens, volumes)" "$(tam "$GRAPH")"
printf '  %-46s %s\n' "versões do agente ($RELEASES)" "$(tam "$RELEASES")"
printf '  %-46s %s\n' "cópias do banco ($BACKUPS)" "$(tam "$BACKUPS")"
printf '  %-46s %s\n' "restos de build do agente (~/.cache)" "$(tam "$HOME"/.cache/patchd-agente.* || true)"
echo
podman system df 2>/dev/null || true
echo

echo "== 1. restos de builds do agente interrompidos"
mapfile -t restos < <(find "$HOME/.cache" -maxdepth 1 -name 'patchd-agente.*' -type d -mmin +60 2>/dev/null)
[ ${#restos[@]} -eq 0 ] && echo "  nenhum"
for d in "${restos[@]}"; do
  echo "  $d ($(tam "$d"))"
  faz rm -rf -- "$d"
done

echo "== 2. camadas de compilação sem nome"
n="$(podman images -q -f dangling=true | wc -l)"
echo "  $n imagem(ns) sem nome"
[ "$n" -gt 0 ] && faz podman image prune -f

echo "== 3. imagens antigas do servidor"
atual="$(val PATCHD_VERSION)"
anterior="$(sed -n 's/^VERSAO_ANTERIOR=//p' deploy/.atualizacao 2>/dev/null | tail -1)"
echo "  ficam: ${atual:-?} (em uso) e ${anterior:-nenhuma} (a do --voltar)"
mapfile -t tags < <(podman images --format '{{.Tag}}' "$IMAGEM" | grep -E '^v[0-9]' | sort -uV)
apagar=()
for t in "${tags[@]}"; do
  [ "$t" = "$atual" ] || [ "$t" = "$anterior" ] || apagar+=("$t")
done
[ ${#apagar[@]} -eq 0 ] && echo "  nenhuma para apagar"
if [ -z "$atual" ] && [ ${#apagar[@]} -gt 0 ]; then
  echo "  PATCHD_VERSION vazio no deploy/.env: não sei qual está em uso; nenhuma sai" >&2
  apagar=()
fi
for t in "${apagar[@]}"; do
  echo "  $IMAGEM:$t"
  faz podman rmi "$IMAGEM:$t"
done

echo "== 4. cache de compilação do Go (builds do agente)"
if podman volume exists patchd-gocache 2>/dev/null; then
  echo "  volume patchd-gocache ($(tam "$(podman volume inspect patchd-gocache --format '{{.Mountpoint}}' 2>/dev/null)"))"
  faz podman volume rm patchd-gocache
else
  echo "  nenhuma"
fi

echo "== 5. versões antigas do agente"
publicada="$(tr -d ' \n' < "$RELEASES/current" 2>/dev/null || true)"
echo "  publicada: ${publicada:-nenhuma}"
mapfile -t versoes < <(find "$RELEASES" -mindepth 1 -maxdepth 1 -type d -name 'v[0-9]*' -printf '%f\n' 2>/dev/null | sort -V)
manter=("$publicada")
outras=()
for v in "${versoes[@]}"; do [ "$v" = "$publicada" ] || outras+=("$v"); done
# As duas mais novas além da publicada ficam (voltar o agente: ./deploy/agente.sh --publicar).
for ((i = ${#outras[@]} - 1, k = 0; i >= 0 && k < 2; i--, k++)); do manter+=("${outras[i]}"); done
sairam=0
for v in "${outras[@]}"; do
  [[ " ${manter[*]} " == *" $v "* ]] && continue
  echo "  $v ($(tam "$RELEASES/$v"))"
  faz rm -rf -- "${RELEASES:?}/$v"
  sairam=1
done
[ "$sairam" = 0 ] && echo "  nenhuma para apagar (ficam: ${manter[*]})"

echo
if $aplicar; then
  echo "== depois"
  df -h "$REPO" "$HOME" ${GRAPH:+"$GRAPH"} "$RELEASES" 2>/dev/null | awk 'NR==1 || !seen[$1]++'
else
  echo "nada foi apagado; para apagar: ./deploy/limpar.sh --aplicar"
fi
b="$(tam "$BACKUPS")"
echo "as cópias do banco ($BACKUPS, ${b:-0}) ficam; se pesam neste disco, aponte PATCHD_BACKUP_DIR no deploy/.env para o volume maior"
