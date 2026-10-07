#!/usr/bin/env bash
# Aula 6.6, parte 4b: o apt da VM Ubuntu usando o patchd-server como cache.
# Roda na VM, com o túnel aberto (ssh -R 18080:127.0.0.1:18080 ...): sudo bash lab-6.6-4b-apt.sh
# Nada é gravado na configuração do apt: o proxy vai só na linha de comando (-o), e as
# listas e os pacotes vão para pastas temporárias, apagadas no fim.
set -uo pipefail
[ "$(id -u)" = 0 ] || { echo "rode com sudo" >&2; exit 1; }
PROXY="${PATCHD_PROXY:-http://127.0.0.1:18080}"
LISTAS=/tmp/lab-listas; PACOTES=/tmp/lab-pacotes
trap 'rm -rf "$LISTAS" "$PACOTES" /tmp/lab-saida.txt' EXIT

hosts=$(grep -hoE '^URIs: *http://[^/ ]+' /etc/apt/sources.list.d/*.sources 2>/dev/null | sed -E 's#.*http://##' | sort -u)
opts=(-o Dir::State::Lists="$LISTAS" -o Dir::Cache::archives="$PACOTES" -o Debug::NoLocking=1 -o Debug::Acquire::http=true)
for h in $hosts; do opts+=(-o "Acquire::http::Proxy::$h=$PROXY"); done

roda() { # roda: título, comando do apt...
  local titulo=$1; shift
  local t0=$(date +%s.%N)
  LC_ALL=C apt-get "${opts[@]}" "$@" > /tmp/lab-saida.txt 2>&1
  local codigo=$? t1=$(date +%s.%N)
  echo "-- $titulo: código $codigo, $(printf '%.1f' "$(echo "$t1 - $t0" | bc)") s"
  tr -d '\r' < /tmp/lab-saida.txt | grep -E '^(Fetched|Need to get|E:)' | sed 's/^/   /'
  tr -d '\r' < /tmp/lab-saida.txt | grep -oE '^X-Patchd-Cache: [A-Z]+' | sort | uniq -c | sed 's/^/   /'
  echo "   respostas: $(tr -d '\r' < /tmp/lab-saida.txt | grep -oE '^HTTP/1\.[01] [0-9]{3}' | sort | uniq -c | tr -s ' ' | tr '\n' ';')"
}

echo "== 0. túnel e origens"
echo "healthz: $(curl -s -o /dev/null -w '%{http_code}' "$PROXY/healthz")"
echo "origens nas fontes (mandadas ao cache): $(echo $hosts | tr ' ' ',')"

echo; echo "== 1. listas: duas vezes do zero"
for vez in 1 2; do rm -rf "$LISTAS"; mkdir -p "$LISTAS/partial"; roda "update $vez" update; done

echo; echo "== 2. pacotes de um dist-upgrade: duas vezes do zero"
for vez in 1 2; do rm -rf "$PACOTES"; mkdir -p "$PACOTES/partial"; roda "download $vez" dist-upgrade --download-only -y; done
echo "   pacotes baixados: $(ls "$PACOTES"/*.deb 2>/dev/null | wc -l)"

echo; echo "== 3. depois de 1 minuto: o InRelease é revalidado na origem"
sleep 65
roda "update 3 (listas já presentes)" update

echo; echo "== 4. o que o cache recusa"
echo "host fora da lista: HTTP $(curl -s -o /dev/null -w '%{http_code}' -x "$PROXY" http://example.com/)"
curl -s -o /dev/null -x "$PROXY" https://archive.ubuntu.com/ 2>/dev/null; echo "CONNECT (https pelo cache): código do curl $? (56 = o proxy recusou o túnel)"
