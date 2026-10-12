#!/usr/bin/env bash
# Prepara os segredos do compose de produção em deploy/secrets (fora do git e do contexto
# do build) e confere o resto do que o compose monta (Aulas 9.3 e 9.4).
# Não sobrescreve nada: o Postgres só lê a senha na primeira inicialização do volume
# (trocar a senha de um banco já criado exige ALTER ROLE), e a chave de jobs tem de ser a
# mesma cuja pública está embutida nos agentes.
set -euo pipefail

DEPLOY="$(cd "$(dirname "$0")" && pwd)"
DIR="$DEPLOY/secrets"
mkdir -p "$DIR"
# Só o dono entra na pasta: protege os arquivos no host.
chmod 700 "$DIR"

# 1. Senha e URL do banco.
if [ -e "$DIR/db_password" ] || [ -e "$DIR/database_url" ]; then
  echo "banco: segredos já existem; nada foi alterado."
else
  # 24 bytes aleatórios em hexadecimal: 48 caracteres, sem símbolos que exijam escape na URL.
  SENHA="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
  printf '%s' "$SENHA" > "$DIR/db_password"
  # sslmode=disable: a conexão não sai da rede interna do compose (TLS no banco: pendência).
  printf 'postgres://patchd:%s@postgres:5432/patchd?sslmode=disable\n' "$SENHA" > "$DIR/database_url"
  # 644: os segredos do compose chegam ao container com o dono e a permissão do host, e
  # o Postgres e o servidor rodam com UIDs diferentes. A pasta 700 protege no host.
  chmod 644 "$DIR/db_password" "$DIR/database_url"
  echo "banco: segredos gerados."
fi

# Arquivos que o servidor (UID 65532) lê. A ACL dá leitura só a ele e mantém o arquivo
# seu: o servidor de desenvolvimento, que roda com o seu usuário, continua lendo o mesmo
# arquivo (Aula 9.3). Sem o pacote acl: sudo chown 65532:65532 ARQUIVO && sudo chmod 400 ARQUIVO.
acl() { echo "    sudo setfacl -m u:65532:r $1"; }

# 2. Segredo do cliente do RHBK (Aula 9.4): o compose monta o arquivo mesmo com o RHBK
# desligado, então ele existe, vazio, até ser preenchido.
if [ ! -e "$DIR/oidc_client_secret" ]; then
  ( umask 077; : > "$DIR/oidc_client_secret" )
  echo "RHBK: $DIR/oidc_client_secret criado vazio (preencha se for ligar o RHBK)."
fi

falta=0
# 3. Chave de jobs: não é gerada aqui. Uma chave nova não vale para os agentes já
# instalados (eles conferem com a pública embutida no build).
if [ -s "$DIR/job_signing_key" ]; then
  echo "chave de jobs: presente."
else
  falta=1
  cat <<MSG
chave de jobs: FALTA $DIR/job_signing_key
  - frota existente: copie a chave privada que o servidor já usa (a do PATCHD_JOB_SIGNING_KEY_FILE);
  - frota nova: go run ./cmd/server job keygen -out deploy/secrets/job_signing_key
    e compile os agentes com a pública que ele imprime (PATCHD_JOB_PUBKEY).
  Depois, para o servidor (UID 65532) ler:
$(acl deploy/secrets/job_signing_key)
MSG
fi

# 4. TLS: certificado da CA interna, em deploy/tls (fora do git).
TLS="$DEPLOY/tls"
if [ -s "$TLS/server.crt" ] && [ -s "$TLS/server.key" ]; then
  echo "TLS: server.crt e server.key presentes."
else
  falta=1
  cat <<MSG
TLS: FALTA $TLS/server.crt e/ou server.key (o certificado, com as intermediárias, e a chave).
  Depois de copiar:
$(acl deploy/tls/server.key)
MSG
fi

# 5. Configuração: deploy/.env (fora do git).
if [ -s "$DEPLOY/.env" ]; then
  echo "configuração: deploy/.env presente."
  # RHBK ligado no .env pede o segredo do cliente preenchido.
  if grep -qE '^PATCHD_OIDC_ISSUER=.+' "$DEPLOY/.env"; then
    if [ -s "$DIR/oidc_client_secret" ]; then
      echo "RHBK: ligado no .env; segredo do cliente presente."
    else
      falta=1
      echo "RHBK: ligado no .env, mas $DIR/oidc_client_secret está vazio (o segredo do cliente no RHBK, aba Credentials)."
      acl deploy/secrets/oidc_client_secret
    fi
  fi
else
  falta=1
  echo "configuração: FALTA deploy/.env (cp deploy/.env.exemplo deploy/.env e preencha)"
fi

if [ "$falta" -ne 0 ]; then
  echo "Complete o que falta antes do docker compose up."
  exit 1
fi
echo "Pronto para: docker compose -f deploy/compose.yaml up -d --build"
