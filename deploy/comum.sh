# Funções comuns dos scripts do compose de produção (Aula 10.4). Carregado com ".", a
# partir da raiz do repositório; não é executado sozinho.
#
# Variáveis PATCHD_* exportadas no shell valem mais que o deploy/.env na interpolação do
# compose: nas Aulas 10.1 e 10.3, o servidor de produção subiu com a configuração do
# desenvolvimento. Todo compose daqui roda sem elas; quem decide é o deploy/.env (e, na
# atualização, só PATCHD_VERSION e PATCHD_COMMIT, passados de propósito).

COMPOSE="${PATCHD_COMPOSE_FILE:-deploy/compose.yaml}"

# sem_patchd CMD...: roda CMD sem nenhuma PATCHD_* do ambiente.
sem_patchd() {
  local tirar=() v
  while IFS= read -r v; do
    tirar+=(-u "$v")
  done < <(compgen -e | grep -E '^PATCHD_' || true)
  env ${tirar[@]+"${tirar[@]}"} "$@"
}

dc() { sem_patchd docker compose -f "$COMPOSE" "$@"; }
pg() { dc exec -T postgres "$@"; }

# dc_versao VERSAO COMMIT ARGS...: compose com a versão explícita (build e troca da imagem).
dc_versao() {
  local v="$1" c="$2"
  shift 2
  sem_patchd env PATCHD_VERSION="$v" PATCHD_COMMIT="$c" docker compose -f "$COMPOSE" "$@"
}

# env_get CHAVE: o valor da chave no deploy/.env (vazio se não houver).
env_get() { sed -n "s/^$1=//p" deploy/.env 2>/dev/null | tail -1; }

# env_set CHAVE VALOR: troca ou acrescenta a linha no deploy/.env. O valor já vem validado
# (versão ou hash), sem caracteres especiais para o sed.
env_set() {
  if grep -q "^$1=" deploy/.env; then
    sed -i "s|^$1=.*|$1=$2|" deploy/.env
  else
    printf '%s=%s\n' "$1" "$2" >> deploy/.env
  fi
}
