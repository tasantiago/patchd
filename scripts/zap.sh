#!/usr/bin/env bash
# Varredura dinâmica do painel com o ZAP (Aula 20.4, S5 do docs/seguranca.md). Roda no HOST
# (precisa do Docker), contra o servidor do laboratório, nunca contra a produção.
#
#   scripts/zap.sh passiva   percorre o painel (spider) e só observa as respostas: cabeçalhos,
#                            cookies, CSP, vazamentos. Não manda formulário. Uns 5 minutos.
#   scripts/zap.sh ativa     além disso, ataca cada parâmetro das páginas que achou (injeção,
#                            XSS, travessia de caminho...). Até ZAP_MINUTOS (padrão 30).
#
# Nos dois modos o spider não envia formulários POST (spider.postform=false): o "Sair" do
# menu encerraria a sessão no meio, e uma sessão de admin aprovaria coisas. As ações (POST)
# ficam com os testes em Go: a matriz de rotas (20.1) e o fuzzing (20.3). Mesmo assim, use
# uma sessão de LEITURA: se algo escapar, o servidor recusa toda ação dela.
#
# Precisa de:
#   PATCHD_ZAP_URL      endereço do painel no lab, como o navegador do host o abre
#                       (ex.: https://127.0.0.1:18443). Nada de host da produção.
#   PATCHD_ZAP_SESSAO   arquivo com o valor do cookie patchd_sessao (padrão: zap/relatorios/.sessao,
#                       fora do git), criado no container, na pasta do repositório:
#                         (umask 077; patchd-server session create -role leitura > zap/relatorios/.sessao)
#                       No fim: patchd-server session end, e apague o arquivo.
#
# O cookie não vai na linha de comando (apareceria no "ps" e no histórico): vai num arquivo de
# variáveis só para o docker, apagado no fim. O relatório fica em zap/relatorios/ (fora do
# git: tem os endereços e as respostas do lab).
#
# Saída: 0 nenhuma regra FAIL (os avisos ficam no relatório); 1 alguma regra FAIL
# (zap/regras.tsv); 3 erro.
set -euo pipefail

cd "$(dirname "$0")/.."
modo="${1:-}"
case "$modo" in
passiva) prog=zap-baseline.py ;;
ativa) prog=zap-full-scan.py ;;
*)
	sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
	exit 3
	;;
esac

url="${PATCHD_ZAP_URL:?defina PATCHD_ZAP_URL (o painel do lab, ex.: https://127.0.0.1:18443)}"
url="${url%/}"
sessao="${PATCHD_ZAP_SESSAO:-zap/relatorios/.sessao}"
imagem="${ZAP_IMAGEM:-ghcr.io/zaproxy/zaproxy:stable}"
minutos="${ZAP_MINUTOS:-30}"

case "$url" in
https://127.0.0.1:* | https://localhost:* | http://127.0.0.1:* | http://localhost:*) ;;
*)
	echo "PATCHD_ZAP_URL=$url não é o lab nesta máquina (127.0.0.1 ou localhost)." >&2
	echo "A varredura ativa manda milhares de pedidos: só contra o lab. Para outro endereço, rode o ZAP à mão." >&2
	exit 3
	;;
esac
if [ ! -s "$sessao" ]; then
	echo "sem o arquivo da sessão ($sessao): no container, na pasta do repositório, rode" >&2
	echo "  mkdir -p zap/relatorios && (umask 077; patchd-server session create -role leitura > zap/relatorios/.sessao)" >&2
	exit 3
fi
valor=$(head -n1 "$sessao" | tr -d '\r\n')
case "$valor" in
patchd_ses_*) ;;
*)
	echo "o arquivo $sessao não tem um valor de sessão do patchd (patchd_ses_...)" >&2
	exit 3
	;;
esac

saida="zap/relatorios/$(date +%Y%m%d-%H%M)-$modo"
mkdir -p "$saida"
cp zap/regras.tsv "$saida/"
chmod a+rwx "$saida" # o ZAP roda no container como o usuário zap (uid 1000)

envfile=$(mktemp)
chmod 600 "$envfile"
trap 'rm -f "$envfile"' EXIT
{
	echo "ZAP_AUTH_HEADER=Cookie"
	echo "ZAP_AUTH_HEADER_VALUE=patchd_sessao=$valor"
} >"$envfile"

opcoes="-config spider.postform=false"
if [ "$modo" = ativa ]; then
	opcoes="$opcoes -config scanner.maxScanDurationInMins=$minutos -config scanner.maxRuleDurationInMins=5"
fi

echo "== ZAP $modo contra $url (relatório em $saida)"
docker pull -q "$imagem" >/dev/null
echo "   imagem: $(docker image inspect --format '{{index .RepoDigests 0}}' "$imagem")"
set +e
docker run --rm --network host --env-file "$envfile" -v "$PWD/$saida:/zap/wrk:rw" -t "$imagem" \
	"$prog" -t "$url/painel/" -c regras.tsv -r relatorio.html -J relatorio.json -w relatorio.md \
	-m 3 -z "$opcoes" -I
rc=$?
set -e
echo
echo "relatório: $saida/relatorio.html (e .md, .json); código $rc"
exit "$rc"
