#!/usr/bin/env bash
# Fuzzing do patchd (Aula 20.3, S3 do docs/seguranca.md): roda cada alvo "func FuzzXxx" do
# código por um tempo e para no primeiro que achar uma entrada que quebra a propriedade.
#
# Uso:
#   scripts/fuzz.sh                    todos os alvos, 30 s cada
#   FUZZTIME=5m scripts/fuzz.sh ber    só os alvos cujo pacote ou nome contém "ber"
#   scripts/fuzz.sh -l                 lista os alvos
#
# Quando um alvo acha algo, o Go grava a entrada em <pacote>/testdata/fuzz/<Alvo>/<hash>.
# Esse arquivo é o teste de regressão: o "go test" comum roda todas as entradas de lá. Faça o
# commit dele junto com a correção, nunca sozinho (o go test ficaria vermelho).
#
# O alvo do PostgreSQL (FuzzSaveInventory) precisa do PATCHD_TEST_DATABASE_URL; sem ele, é
# pulado. O corpus que o fuzzer acumula fica no cache do Go ($(go env GOCACHE)/fuzz), fora
# do repositório: rodar de novo continua de onde parou.
set -euo pipefail

cd "$(dirname "$0")/.."
tempo="${FUZZTIME:-30s}"
filtro="${1:-}"

# Os alvos saem do código: alvo novo entra sozinho, nenhum fica esquecido.
alvos=$(grep -rl --include='*_test.go' '^func Fuzz' internal cmd | sort | while read -r f; do
	grep -o '^func Fuzz[A-Za-z0-9_]*' "$f" | sed "s|^func |./$(dirname "$f") |"
done)

if [ "$filtro" = "-l" ]; then
	echo "$alvos"
	exit 0
fi

falhas=()
n=0
pulados=0
while read -r pkg alvo; do
	[ -n "$pkg" ] || continue
	case "$pkg $alvo" in *"$filtro"*) ;; *) continue ;; esac
	n=$((n + 1))
	echo "== $alvo ($pkg, $tempo)"
	if out=$(go test -v -run '^$' -fuzz "^${alvo}\$" -fuzztime "$tempo" "$pkg" 2>&1); then
		if echo "$out" | grep -q -- '--- SKIP'; then
			echo "$out" | grep -B1 -- '--- SKIP' | head -1 | sed 's/^ *[a-z_]*\.go:[0-9]*: /   pulado: /'
			pulados=$((pulados + 1))
		else
			echo "$out" | grep -E '^ok' | tail -1 | sed 's/^/   /'
		fi
	else
		echo "$out" | grep -v '^fuzz: elapsed' | tail -15
		falhas+=("$alvo")
	fi
done <<<"$alvos"

echo
if [ "$n" -eq 0 ]; then
	echo "nenhum alvo com \"$filtro\" (scripts/fuzz.sh -l lista os alvos)"
	exit 2
fi
if [ ${#falhas[@]} -gt 0 ]; then
	echo "achados em: ${falhas[*]} (a entrada ficou em testdata/fuzz/ do pacote)"
	exit 1
fi
echo "fuzzing: $n alvos ($pulados pulados), nada encontrado em $tempo cada"
