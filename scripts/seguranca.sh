#!/usr/bin/env bash
# Análise estática de segurança do patchd (Aula 20.2, S2 do docs/seguranca.md).
#
#   govulncheck  vulnerabilidades conhecidas que o código ALCANÇA (biblioteca padrão e módulos)
#   staticcheck  erros e código morto que o go vet não pega
#   gosec        padrões de risco (permissões, SSRF, caminhos, conversões, cookies...)
#
# As três rodam para linux, windows e darwin: muito do agente só existe num dos três.
# Sai com código diferente de zero quando alguma acha algo, para servir de portão no CI.
#
# Uso:
#   scripts/seguranca.sh                 instala o que faltar e roda as três
#   scripts/seguranca.sh gosec           só uma (govulncheck, staticcheck ou gosec)
#   SO="linux" scripts/seguranca.sh      só um sistema
#
# As versões são fixas: atualizar uma ferramenta é um commit, e o achado novo que ela trouxer
# aparece nesse commit, não no meio de outro trabalho. As ferramentas ficam fora do
# repositório, numa pasta por versão (~/.cache/patchd-seguranca).
#
# Exceções: o gosec lê o .gosec.json (G304 desligada, porque a G703 cobre caminho vindo de
# fora; G103 nos arquivos _windows.go; a lista do G104). Toda outra exceção é um
# "// #nosec Gxxx -- motivo" na linha, e o motivo é obrigatório.
set -euo pipefail

GOVULNCHECK_VERSION=v1.9.0
STATICCHECK_VERSION=v0.8.2 # a versão 2026.2.2 do staticcheck
GOSEC_VERSION=v2.29.0

cd "$(dirname "$0")/.."
cache="${XDG_CACHE_HOME:-$HOME/.cache}/patchd-seguranca"

# instalar NOME MÓDULO VERSÃO: go install numa pasta só daquela versão.
instalar() {
	local nome=$1 mod=$2 ver=$3
	local dir="$cache/$nome-$ver"
	if [ ! -x "$dir/$nome" ]; then
		echo "== instalando $nome $ver" >&2
		mkdir -p "$dir"
		GOBIN="$dir" GOOS= GOARCH= go install "$mod@$ver" >&2
	fi
	echo "$dir/$nome"
}

quais="${1:-govulncheck staticcheck gosec}"
sistemas="${SO:-linux windows darwin}"
falhas=()

for ferramenta in $quais; do
	case $ferramenta in
	govulncheck) bin=$(instalar govulncheck golang.org/x/vuln/cmd/govulncheck "$GOVULNCHECK_VERSION") ;;
	staticcheck) bin=$(instalar staticcheck honnef.co/go/tools/cmd/staticcheck "$STATICCHECK_VERSION") ;;
	gosec) bin=$(instalar gosec github.com/securego/gosec/v2/cmd/gosec "$GOSEC_VERSION") ;;
	*)
		echo "ferramenta desconhecida: $ferramenta (govulncheck, staticcheck ou gosec)" >&2
		exit 2
		;;
	esac
	for so in $sistemas; do
		echo "== $ferramenta ($so)"
		case $ferramenta in
		govulncheck) cmd=("$bin" ./...) ;;
		staticcheck) cmd=("$bin" ./...) ;;
		gosec) cmd=("$bin" -quiet -conf .gosec.json ./...) ;;
		esac
		if GOOS=$so GOARCH=amd64 CGO_ENABLED=0 "${cmd[@]}"; then
			echo "   nada encontrado"
		else
			falhas+=("$ferramenta/$so")
		fi
	done
done

echo
if [ ${#falhas[@]} -gt 0 ]; then
	echo "achados em: ${falhas[*]}"
	exit 1
fi
echo "análise de segurança: nada encontrado ($quais; $sistemas)"
