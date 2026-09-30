#!/usr/bin/env bash
# Gera os segredos do compose de produção em deploy/secrets (fora do git e do contexto do build).
# Não sobrescreve segredos existentes: o Postgres só lê a senha na primeira inicialização do volume;
# trocar a senha de um banco já criado exige ALTER ROLE.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)/secrets"
mkdir -p "$DIR"
# Só o dono entra na pasta: protege os arquivos no host.
chmod 700 "$DIR"

if [ -e "$DIR/db_password" ] || [ -e "$DIR/database_url" ]; then
  echo "Segredos já existem em $DIR; nada foi alterado."
  exit 0
fi

# 24 bytes aleatórios em hexadecimal: 48 caracteres, sem símbolos que exijam escape na URL.
SENHA="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"

printf '%s' "$SENHA" > "$DIR/db_password"
# sslmode=disable: a conexão fica dentro da rede interna do compose; TLS no banco entra na Aula 9.1.
printf 'postgres://patchd:%s@postgres:5432/patchd?sslmode=disable\n' "$SENHA" > "$DIR/database_url"

# 644: os segredos do compose chegam ao container com o dono e a permissão do host,
# e os processos dos containers não rodam com o seu UID. A pasta 700 protege no host.
chmod 644 "$DIR/db_password" "$DIR/database_url"

echo "Segredos gerados em $DIR."
