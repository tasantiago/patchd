#!/usr/bin/env bash
# Pacote para revisão externa (Aula 20.8, S9 do docs/seguranca.md): tudo o que quem revisa
# precisa, num arquivo só, e nada do ambiente real.
#
# Uso (dentro do container, com a árvore limpa e depois do scripts/build.sh da mesma versão):
#   scripts/revisao.sh v0.17.11
#
# Monta revisao/patchd-revisao-VERSÃO/ e revisao/patchd-revisao-VERSÃO.tar.gz (+ .sha256):
#   LEIAME.md          o que é cada coisa, como reproduzir, o que ficou de fora
#   codigo-*.tar.gz    o código do commit (git archive: sem o histórico do git)
#   sbom/              CycloneDX 1.5 de cada executável do dist/ e modulos.txt (cmd/sbom)
#   binarios.sha256    os SHA-256 dos executáveis analisados (os executáveis não vão)
#   resultados/        seguranca.sh, go test, fuzz.sh e, com ZAP=, o relatório do ZAP
#   docs/              seguranca.md (modelo de ameaças e achados) e rotas.md
#
# Antes de empacotar, procura no pacote os termos que nunca podem sair (regra do repositório):
#   - os do arquivo PATCHD_PROIBIDOS (padrão ~/.config/patchd/proibidos.txt, obrigatório,
#     fora do repositório): um termo por linha (domínio, nomes de host, prefixo de IP...), #
#     comenta, e "env:NOME" puxa o valor de NOME do .env ou do deploy/.env;
#   - o conteúdo de cada arquivo de deploy/secrets/ e do cookie do ZAP (zap/relatorios/.sessao);
#   - chaves privadas (patchd_relkey_, patchd_jobkey_, AGE-SECRET-KEY-, BEGIN ... PRIVATE KEY).
# Achou: diz qual termo (pelo número da linha ou pela origem, nunca o valor) e em qual arquivo,
# e não gera o .tar.gz. Também conta em quantos commits do histórico do git cada termo do
# arquivo aparece (o pacote não leva o histórico; reescrevê-lo é outra decisão).
#
# Variáveis: PULAR="testes fuzz seguranca" pula etapas; FUZZTIME (padrão 30s por alvo);
# ZAP=zap/relatorios/AAAAMMDD-HHMM-modo inclui um relatório do scripts/zap.sh.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
VERSAO="${1:-}"
if ! [[ "$VERSAO" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "uso: scripts/revisao.sh VERSÃO   (exemplo: scripts/revisao.sh v0.17.11)" >&2
  exit 2
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "ERRO: a árvore tem alterações não commitadas; o pacote é de um commit." >&2
  exit 1
fi
PROIBIDOS="${PATCHD_PROIBIDOS:-$HOME/.config/patchd/proibidos.txt}"
if ! grep -qv '^[[:space:]]*\(#\|$\)' "$PROIBIDOS" 2>/dev/null; then
  echo "ERRO: sem a lista de termos proibidos ($PROIBIDOS, ou PATCHD_PROIBIDOS)." >&2
  echo "Crie fora do repositório, um termo por linha: o domínio, os nomes de host, o prefixo" >&2
  echo "dos IPs internos, e linhas env:NOME para valores do .env (veja docs/operacao.md)." >&2
  exit 1
fi
COMMIT="$(git rev-parse HEAD)"
NOME="patchd-revisao-$VERSAO"
SAIDA="revisao/$NOME"
if [ -e "$SAIDA" ]; then
  echo "ERRO: $SAIDA já existe (apague para refazer)." >&2
  exit 1
fi
pular() { [[ " ${PULAR:-} " == *" $1 "* ]]; }

# Os executáveis do dist/: desta versão (a string está dentro) e deste commit (o cmd/sbom confere).
mapfile -t exes < <(ls dist/patchd-agent-* dist/patchd-server-* 2>/dev/null)
if [ ${#exes[@]} -eq 0 ]; then
  echo "ERRO: dist/ sem executáveis; rode ./scripts/build.sh $VERSAO antes." >&2
  exit 1
fi
for e in "${exes[@]}"; do
  grep -qF -- "$VERSAO" "$e" || { echo "ERRO: $e não é da versão $VERSAO; rode ./scripts/build.sh $VERSAO." >&2; exit 1; }
done

trab="$(mktemp -d)"
trap 'rm -rf "$trab"' EXIT
chmod 700 "$trab"
mkdir -p "$SAIDA/resultados" "$SAIDA/docs"

echo "== código ($COMMIT)"
git archive --format=tar.gz --prefix="patchd-$VERSAO/" -o "$SAIDA/codigo-$VERSAO.tar.gz" HEAD

echo "== SBOM (${#exes[@]} executáveis)"
go run ./cmd/sbom -out "$SAIDA/sbom" -version "$VERSAO" -revision "$COMMIT" "${exes[@]}"
(cd dist && sha256sum -- $(for e in "${exes[@]}"; do basename "$e"; done)) > "$SAIDA/binarios.sha256"

cp docs/seguranca.md docs/rotas.md "$SAIDA/docs/"

# etapa NOME ARQUIVO "texto se passar" COMANDO...: roda, grava a saída e o resultado.
declare -A resultado
etapa() {
  local nome=$1 arq=$2 ok=$3
  shift 3
  if pular "$nome"; then
    resultado[$nome]="pulado"
    echo "pulado (PULAR)" > "$SAIDA/resultados/$arq"
    return
  fi
  echo "== $nome"
  if "$@" > "$SAIDA/resultados/$arq" 2>&1; then resultado[$nome]="$ok"; else resultado[$nome]="FALHOU (código $?)"; fi
  echo "   ${resultado[$nome]}"
}
db="não"
[ -n "${PATCHD_TEST_DATABASE_URL:-}" ] && db="sim"
etapa seguranca seguranca.txt "nada encontrado" ./scripts/seguranca.sh
etapa testes testes.txt "todos passaram" go test -count=1 ./...
FUZZTIME="${FUZZTIME:-30s}" etapa fuzz fuzz.txt "nada encontrado" ./scripts/fuzz.sh
if [ -n "${ZAP:-}" ]; then
  [ -d "$ZAP" ] || { echo "ERRO: ZAP=$ZAP não é uma pasta" >&2; exit 1; }
  mkdir -p "$SAIDA/resultados/zap"
  cp "$ZAP"/relatorio.* "$SAIDA/resultados/zap/"
  resultado[zap]="relatório de $(basename "$ZAP")"
else
  resultado[zap]="não incluído (seção 5c do docs/seguranca.md)"
fi

{
  echo "commit: $COMMIT"
  echo "versão: $VERSAO"
  echo "gerado em: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "go: $(go version | cut -d' ' -f3-)"
  echo "testes com PostgreSQL (PATCHD_TEST_DATABASE_URL definida): $db"
  echo "fuzz: ${FUZZTIME:-30s} por alvo"
  grep -E '^(GOVULNCHECK|STATICCHECK|GOSEC)_VERSION=' scripts/seguranca.sh | sed 's/ *#.*//'
} > "$SAIDA/resultados/ambiente.txt"

gover="$(go env GOVERSION)"
gover="${gover#go}"
cat > "$SAIDA/LEIAME.md" <<LEIAME
# patchd $VERSAO: pacote para revisão externa

Commit \`$COMMIT\`. O patchd é um gerenciador de patches: um servidor (Go, PostgreSQL, painel
web) e um agente para Windows, Linux e macOS que roda como SYSTEM/root nas máquinas. Comece
por \`docs/seguranca.md\`: ativos, atores, fronteiras, ameaças por fronteira (STRIDE), os
achados (F1 a F6) e o que cada aula do bloco de segurança mudou.

## Conteúdo

| Item | O quê |
|---|---|
| \`codigo-$VERSAO.tar.gz\` | o código deste commit (\`git archive\`; sem o histórico do git) |
| \`sbom/*.cdx.json\` | CycloneDX 1.5 de cada executável, lido do próprio executável (módulos, versões, hashes do go.sum, versão do Go, opções do build) |
| \`sbom/modulos.txt\` | todos os módulos, com os executáveis que usam cada um |
| \`binarios.sha256\` | os executáveis analisados (não vão no pacote; o SBOM tem o mesmo SHA-256) |
| \`docs/seguranca.md\` | modelo de ameaças, achados e controles |
| \`docs/rotas.md\` | todas as rotas HTTP, com a autenticação de cada uma, gerada do código e testada |
| \`resultados/ambiente.txt\` | versões do Go e das ferramentas |
| \`resultados/seguranca.txt\` | govulncheck, staticcheck e gosec, para linux, windows e darwin: ${resultado[seguranca]} |
| \`resultados/testes.txt\` | \`go test ./...\` (com PostgreSQL: $db): ${resultado[testes]} |
| \`resultados/fuzz.txt\` | \`scripts/fuzz.sh\`, todos os alvos: ${resultado[fuzz]} |
| \`resultados/zap/\` | varredura dinâmica (ZAP) do painel: ${resultado[zap]} |

## Reproduzir

Com Go $gover ou mais novo, a partir do código:

\`\`\`bash
tar xzf codigo-$VERSAO.tar.gz && cd patchd-$VERSAO
./scripts/seguranca.sh                         # as três ferramentas, versões fixas no script
go test ./...                                  # PATCHD_TEST_DATABASE_URL liga os testes do PostgreSQL
./scripts/fuzz.sh                              # FUZZTIME=5m para mais tempo por alvo
\`\`\`

## Fora do pacote, de propósito

- Segredos e configuração do ambiente (chaves, senhas, \`.env\`, certificados): nunca entram
  no repositório. Este pacote foi conferido contra a lista de termos do ambiente real antes
  de ser gerado.
- O histórico do git.
- Os executáveis: o SHA-256 de cada um está em \`binarios.sha256\` e no SBOM. Eles embutem as
  chaves públicas do ambiente; um build a partir do código dá outros hashes.

Dúvidas e achados: pelo canal combinado com quem enviou o pacote.
LEIAME

echo "== termos proibidos"
termos="$trab/termos"; origens="$trab/origens"
: > "$termos"; : > "$origens"
add() { printf '%s\n' "$1" >> "$termos"; printf '%s\n' "$2" >> "$origens"; }
envval() { # valor de NOME no .env ou no deploy/.env (sem aspas)
  local f
  for f in .env deploy/.env; do
    [ -f "$f" ] || continue
    sed -n "s/^\(export \)\{0,1\}$1=//p" "$f" | tail -1 | sed "s/^['\"]//; s/['\"]$//"
  done | grep -m1 . || true
}
n=0
while IFS= read -r linha || [ -n "$linha" ]; do
  n=$((n + 1))
  linha="${linha%$'\r'}"
  [[ "$linha" =~ ^[[:space:]]*(#|$) ]] && continue
  if [[ "$linha" == env:* ]]; then
    v="$(envval "${linha#env:}")"
    if [ -z "$v" ]; then echo "   aviso: linha $n (${linha}) não está no .env nem no deploy/.env"; continue; fi
    add "$v" "linha $n do arquivo de termos, ${linha}"
  elif [ ${#linha} -lt 3 ]; then
    echo "   aviso: linha $n tem menos de 3 caracteres; ignorada"
  else
    add "$linha" "linha $n do arquivo de termos"
  fi
done < "$PROIBIDOS"
nlista=$(wc -l < "$termos")
for f in deploy/secrets/* zap/relatorios/.sessao; do
  [ -f "$f" ] || continue
  v="$(head -c 4096 "$f" | grep -m1 -v '^[[:space:]]*\(#\|$\)' || true)"
  [ ${#v} -ge 8 ] && add "$v" "conteúdo de $f"
done
mkdir "$trab/codigo"
tar xzf "$SAIDA/codigo-$VERSAO.tar.gz" -C "$trab/codigo"
achou=0
i=0
while IFS= read -r t; do
  i=$((i + 1))
  o="$(sed -n "${i}p" "$origens")"
  arqs="$( (grep -rIlFi -e "$t" "$SAIDA" "$trab/codigo" || true) | sed "s|^$trab/codigo/|código: |")"
  if [ -n "$arqs" ]; then
    achou=1
    echo "   ACHADO ($o) em:"
    echo "$arqs" | sed 's/^/     /'
  fi
done < "$termos"
chaves='patchd_(relkey|jobkey)_[A-Za-z0-9_-]{43}|AGE-SECRET-KEY-1[0-9A-Z]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----'
arqs="$( (grep -rIlE -e "$chaves" "$SAIDA" "$trab/codigo" || true) | sed "s|^$trab/codigo/|código: |")"
if [ -n "$arqs" ]; then
  achou=1
  echo "   ACHADO: chave privada em:"
  echo "$arqs" | sed 's/^/     /'
fi
echo "   $(wc -l < "$termos") termo(s) e o padrão das chaves privadas: $([ $achou -eq 0 ] && echo nada encontrado || echo ACHADOS)"

echo "== histórico do git (informativo: o pacote não leva o histórico)"
i=0
hist=0
while IFS= read -r t && [ $i -lt "$nlista" ]; do
  i=$((i + 1))
  c=$(git log --all --format=%h -S"$t" | wc -l)
  if [ "$c" -gt 0 ]; then
    hist=1
    echo "   $(sed -n "${i}p" "$origens"): em $c commit(s)"
  fi
done < "$termos"
[ $hist -eq 0 ] && echo "   nenhum termo do arquivo aparece no histórico"

if [ $achou -ne 0 ]; then
  echo
  echo "o pacote NÃO foi fechado: tire os termos dos arquivos acima (no repositório, com commit) e"
  echo "rode de novo (apague $SAIDA antes)."
  exit 1
fi
(cd "$SAIDA" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
tar czf "revisao/$NOME.tar.gz" -C revisao "$NOME"
(cd revisao && sha256sum "$NOME.tar.gz" > "$NOME.tar.gz.sha256")
echo
for k in seguranca testes fuzz; do echo "$k: ${resultado[$k]}"; done
echo "pacote: revisao/$NOME.tar.gz ($(du -h "revisao/$NOME.tar.gz" | cut -f1)); SHA-256 em revisao/$NOME.tar.gz.sha256"
for k in seguranca testes fuzz; do
  [[ "${resultado[$k]}" == FALHOU* ]] && { echo "atenção: $k falhou; veja resultados/ antes de enviar"; exit 1; }
done
exit 0
