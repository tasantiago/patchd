#!/usr/bin/env bash
# Prepara os segredos do compose de produção em deploy/secrets (fora do git e do contexto
# do build) e confere o resto do que o compose monta (Aulas 9.3, 9.4, 10.2 e 10.3).
# Não sobrescreve nada: o Postgres só lê a senha na primeira inicialização do volume
# (trocar a senha de um banco já criado exige ALTER ROLE), e a chave de jobs tem de ser a
# mesma cuja pública está embutida nos agentes.
set -euo pipefail

DEPLOY="$(cd "$(dirname "$0")" && pwd)"
DIR="$DEPLOY/secrets"
mkdir -p "$DIR"
# Só o dono entra na pasta: protege os arquivos no host.
chmod 700 "$DIR"

# 0. Variáveis PATCHD_* exportadas no shell valem mais que o deploy/.env no compose (Aula
# 10.1: o servidor subiu com o RHBK do ambiente de desenvolvimento). Só os nomes, nunca os valores.
# Desde a Aula 10.4 a versão também mora no deploy/.env (o atualizar.sh anota): nenhuma
# PATCHD_* exportada é inofensiva para um "docker compose" digitado à mão.
exportadas="$(env | grep -oE '^PATCHD_[A-Z0-9_]+' | sort | tr '\n' ' ' | sed 's/ $//' || true)"
if [ -n "$exportadas" ]; then
  echo "AVISO: PATCHD_* exportadas neste shell passam por cima do deploy/.env no compose:"
  echo "    $exportadas"
  echo "  Use ./deploy/compose.sh (roda o docker compose sem elas) e ./deploy/atualizar.sh."
fi

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

# 2a. Senha do SMTP (Aula 18.5): como o do RHBK, existe vazio até ser preenchido; só é lida
# com PATCHD_SMTP_USER definido.
if [ ! -e "$DIR/smtp_password" ]; then
  ( umask 077; : > "$DIR/smtp_password" )
  echo "SMTP: $DIR/smtp_password criado vazio (preencha se o SMTP pedir login)."
fi

# 2b. Token das métricas (Aula 10.2): gerado aqui, nunca escolhido. O mesmo valor vai
# para o Prometheus (credentials_file); trocar = gerar outro e atualizar os dois lados.
if [ -s "$DIR/metrics_token" ]; then
  echo "métricas: token presente."
else
  ( umask 077; od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$DIR/metrics_token"; echo >> "$DIR/metrics_token" )
  echo "métricas: token gerado em $DIR/metrics_token ($(wc -c < "$DIR/metrics_token" | tr -d ' ') bytes)."
  # O dono do arquivo pode dar a ACL sem sudo.
  if command -v setfacl >/dev/null 2>&1 && setfacl -m u:65532:r "$DIR/metrics_token" 2>/dev/null; then
    echo "métricas: leitura liberada para o servidor (UID 65532) por ACL."
  else
    echo "métricas: libere a leitura para o servidor (UID 65532):"
    acl deploy/secrets/metrics_token
  fi
fi

falta=0
# 2c. Cópias cifradas (Aula 10.3): aqui fica só a chave PÚBLICA do age; a privada, fora.
REC="$DIR/backup_recipients"
if [ -s "$REC" ]; then
  if grep -q 'AGE-SECRET-KEY-' "$REC"; then
    falta=1
    echo "cópias: $REC tem uma chave PRIVADA do age. Deixe só a linha age1… e guarde a privada fora do servidor."
  elif ! command -v age >/dev/null 2>&1; then
    falta=1
    echo "cópias: cifradas no deploy/secrets, mas FALTA o age neste host (Ubuntu/Debian: sudo apt install age)."
  else
    echo "cópias: cifradas com age para $(grep -cvE '^[[:space:]]*(#|$)' "$REC") destinatário(s)."
  fi
else
  echo "cópias: SEM cifra. Para cifrar: age-keygen fora do servidor e a linha age1… em deploy/secrets/backup_recipients (docs/operacao.md)."
fi

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
  # SMTP com login pede a senha preenchida.
  if grep -qE '^PATCHD_SMTP_USER=.+' "$DEPLOY/.env" && [ ! -s "$DIR/smtp_password" ]; then
    falta=1
    echo "SMTP: PATCHD_SMTP_USER no .env, mas $DIR/smtp_password está vazio."
    acl deploy/secrets/smtp_password
  fi
else
  falta=1
  echo "configuração: FALTA deploy/.env (cp deploy/.env.exemplo deploy/.env e preencha)"
fi

if [ "$falta" -ne 0 ]; then
  echo "Complete o que falta antes do docker compose up."
  exit 1
fi
echo "Pronto para: ./deploy/atualizar.sh vX.Y.Z (a primeira subida também: constrói, copia o banco e sobe)"
