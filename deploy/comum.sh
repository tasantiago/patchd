# Funções comuns dos scripts de produção (Aulas 10.4 e 23.1). Carregado com ".", a partir da
# raiz do repositório; não é executado sozinho.
#
# Variáveis PATCHD_* exportadas no shell valem mais que o deploy/.env na interpolação do
# compose: nas Aulas 10.1 e 10.3, o servidor de produção subiu com a configuração do
# desenvolvimento. Todo compose daqui roda sem elas; quem decide é o deploy/.env (e, na
# atualização, só PATCHD_VERSION e PATCHD_COMMIT, passados de propósito).
#
# Motor (Aula 23.1): PATCHD_MOTOR no deploy/.env. "compose" (o padrão): Docker com o
# deploy/compose.yaml. "podman": Podman sem root, com as unidades do Quadlet que o
# deploy/podman/instalar.sh gera (RHEL). Os scripts usam só as funções abaixo, nunca o
# docker ou o podman direto.

COMPOSE="${PATCHD_COMPOSE_FILE:-deploy/compose.yaml}"

# Aula 18.5: o compose monta deploy/secrets/smtp_password mesmo com as notificações
# desligadas; o arquivo nasce vazio (preencha só se o SMTP pedir login).
if [ -d deploy/secrets ] && [ ! -e deploy/secrets/smtp_password ]; then
  ( umask 077; : > deploy/secrets/smtp_password )
fi

# sem_patchd CMD...: roda CMD sem nenhuma PATCHD_* do ambiente.
sem_patchd() {
  local tirar=() v
  while IFS= read -r v; do
    tirar+=(-u "$v")
  done < <(compgen -e | grep -E '^PATCHD_' || true)
  env ${tirar[@]+"${tirar[@]}"} "$@"
}

# env_get CHAVE: o valor da chave no deploy/.env (vazio se não houver).
env_get() { sed -n "s/^$1=//p" deploy/.env 2>/dev/null | tail -1; }
# env_tem CHAVE: a chave está no deploy/.env (mesmo vazia).
env_tem() { grep -q "^$1=" deploy/.env 2>/dev/null; }

# env_set CHAVE VALOR: troca ou acrescenta a linha no deploy/.env. O valor já vem validado
# (versão ou hash), sem caracteres especiais para o sed.
env_set() {
  if grep -q "^$1=" deploy/.env; then
    sed -i "s|^$1=.*|$1=$2|" deploy/.env
  else
    printf '%s=%s\n' "$1" "$2" >> deploy/.env
  fi
}

# dc_versao VERSAO COMMIT ARGS...: compose com a versão explícita (build e troca da imagem).
dc_versao() {
  local v="$1" c="$2"
  shift 2
  sem_patchd env PATCHD_VERSION="$v" PATCHD_COMMIT="$c" docker compose -f "$COMPOSE" "$@"
}
dc() { sem_patchd docker compose -f "$COMPOSE" "$@"; }

MOTOR="$(env_get PATCHD_MOTOR)"
MOTOR="${MOTOR:-compose}"

# O que os scripts usam (os dois motores têm as mesmas):
#   IMAGEM                    o nome da imagem do servidor, sem a tag
#   motor ARGS...             docker ou podman (inspect, image inspect, run)
#   pg CMD...                 CMD dentro do container do Postgres, com a entrada padrão
#   banco_subir               sobe só o Postgres e espera ficar saudável
#   marcar_versao VER COMMIT  anota a versão no .env; é a que a próxima subida usa
#   servidor_trocar           (re)cria o servidor na versão marcada e espera o healthcheck
#   servidor_iniciar          sobe o servidor parado, na versão marcada
#   servidor_parar            para o servidor (o banco continua)
#   servidor_container        id ou nome do container do servidor no ar (vazio: nenhum)
#   servidor_log N            as últimas N linhas do log do servidor
#   construir VER COMMIT      constrói IMAGEM:VER do commit atual
#   DICA_LOG                  o comando do log, para as mensagens
case "$MOTOR" in
  compose)
    IMAGEM=patchd-server
    DICA_LOG="./deploy/compose.sh logs --tail 30 server"
    motor() { docker "$@"; }
    pg() { dc exec -T postgres "$@"; }
    banco_subir() { dc up -d --wait postgres; }
    marcar_versao() { env_set PATCHD_VERSION "$1"; env_set PATCHD_COMMIT "$2"; }
    servidor_trocar() { dc up -d --wait --wait-timeout 180 server; }
    servidor_iniciar() { dc up -d server; }
    servidor_parar() { dc stop server; }
    servidor_container() { dc ps -q server 2>/dev/null || true; }
    servidor_log() { dc logs --tail "$1" server; }
    construir() { dc_versao "$1" "$2" build server; }
    ;;
  podman)
    # Podman sem root: os containers, as imagens e as unidades são do usuário do serviço.
    # Como root, o podman veria outro armazenamento (vazio) e o systemctl --user, nada.
    # (COMUM_SO_LEITURA=true: quem só lê o .env, como o instalar.sh --env, pula a conferência.)
    if [ "${COMUM_SO_LEITURA:-}" != true ]; then
      if [ "$(id -u)" = 0 ]; then
        echo "PATCHD_MOTOR=podman: rode como o usuário do serviço (sudo -iu USUARIO), não como root" >&2
        exit 1
      fi
      # sudo -iu não abre sessão: sem XDG_RUNTIME_DIR, o systemctl --user não acha o systemd
      # do usuário e o podman usaria outra pasta de estado.
      if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -d "/run/user/$(id -u)" ]; then
        export XDG_RUNTIME_DIR="/run/user/$(id -u)"
      fi
    fi
    IMAGEM=localhost/patchd-server
    DICA_LOG="journalctl --user -u patchd-server -n 30"
    motor() { podman "$@"; }
    pg() { podman exec -i patchd-postgres "$@"; }
    # reset-failed (Aula 23.1b): depois de algumas falhas seguidas, o systemd recusa novas
    # tentativas ("Start request repeated too quickly") até alguém zerar a contagem.
    banco_subir() {
      systemctl --user reset-failed patchd-pod.service patchd-postgres.service 2>/dev/null || true
      systemctl --user start patchd-postgres.service
    }
    # A unidade do Quadlet sobe sempre IMAGEM:atual; trocar de versão é mudar essa tag.
    marcar_versao() {
      podman tag "$IMAGEM:$1" "$IMAGEM:atual"
      env_set PATCHD_VERSION "$1"; env_set PATCHD_COMMIT "$2"
    }
    # Notify=healthy na unidade: o restart só volta quando o healthcheck passa (ou falha
    # pelo TimeoutStartSec).
    servidor_trocar() {
      systemctl --user reset-failed patchd-server.service 2>/dev/null || true
      systemctl --user restart patchd-server.service
    }
    servidor_iniciar() {
      systemctl --user reset-failed patchd-server.service 2>/dev/null || true
      systemctl --user start patchd-server.service
    }
    servidor_parar() { systemctl --user stop patchd-server.service; }
    servidor_container() {
      if podman container exists patchd-server; then echo patchd-server; fi
    }
    servidor_log() { journalctl --user -u patchd-server.service -n "$1" --no-pager -o cat; }
    # --format docker: o formato OCI descarta o HEALTHCHECK do Dockerfile. A unidade também
    # declara o seu, mas a imagem fica igual à do compose.
    construir() {
      sem_patchd podman build --format docker --build-arg VERSION="$1" --build-arg COMMIT="$2" \
        -f deploy/Dockerfile -t "$IMAGEM:$1" .
    }
    ;;
  *)
    echo "PATCHD_MOTOR=$MOTOR no deploy/.env: use compose ou podman" >&2
    exit 2
    ;;
esac
