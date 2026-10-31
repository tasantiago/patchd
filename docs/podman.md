# patchd no RHEL com Podman e Quadlet

Para instalar uma **VM nova do zero**, siga o [roteiro completo de Podman no
README](../README.md#instalação-com-podman-sem-root-e-quadlet-rhel-10): preparação
como root, usuário do serviço, build, chave de jobs, TLS, primeiro administrador e
backup. Este guia complementa esse roteiro com detalhes de operação e agentes.
Nenhum passo depende de extrair arquivos do contêiner de desenvolvimento.

O mesmo servidor do `deploy/compose.yaml`, no Podman sem root e com unidades do systemd geradas
pelo Quadlet: o caminho que a Red Hat documenta e suporta no RHEL 10. Nada aqui tem nome de
host, IP ou segredo do ambiente real: esses valores ficam no `deploy/.env` e em
`deploy/secrets`, fora do git.

## Como fica

- Um **usuário do serviço** comum (sem sudo) é dono do clone do repositório, das imagens, dos
  volumes e das unidades. Com *linger*, o systemd dele sobe os serviços no boot, sem ninguém
  logado.
- Um **pod** `patchd` com dois containers: `patchd-postgres` (a imagem oficial do Postgres, a
  mesma do compose) e `patchd-server` (a imagem do `deploy/Dockerfile`). Os dois dividem a rede
  do pod: o servidor fala com o banco em 127.0.0.1 (o nome `postgres` da URL do banco aponta
  para lá), e o banco não tem porta no host.
- **Rede pasta**, não bridge: sem root, uma rede bridge passa as conexões por um proxy que troca
  o IP de origem, e todas as máquinas chegariam com o mesmo IP. O "último IP" de cada máquina,
  o Wake-on-LAN, a trava de login por IP e a auditoria precisam do IP de verdade.
- **keep-id**: o usuário do serviço vira o UID 65532 lá dentro, o do servidor. Os segredos e o
  TLS são arquivos dele, `600`, lidos sem ACL. O root dos containers é um UID sem poder no host.
- **SELinux** ligado: as montagens levam `:Z` (rótulo só daquele pod).
- Serviços do systemd do usuário: `patchd-pod`, `patchd-postgres`, `patchd-server` e o timer
  `patchd-backup`. Com `Notify=healthy`, o systemd só dá o banco e o servidor por iniciados
  quando o healthcheck passa.

O `PATCHD_MOTOR=podman` no `deploy/.env` faz os scripts do `deploy/` usarem o Podman: o
`atualizar.sh`, o `backup.sh`, o `restaurar.sh` e o `gerar-segredos.sh` são os mesmos, com os
mesmos comandos. O `compose.sh` não se aplica (ele mostra os equivalentes).

## Preparação da VM (como root, uma vez)

O `deploy/podman/preparar-vm.sh` (Aula 23.1b) faz tudo desta seção e confere o que já
estava feito; rodar de novo não desfaz nada. Ele vem antes do clone do usuário do serviço:

```sh
dnf install -y git
git clone https://github.com/tasantiago/patchd.git /tmp/patchd
bash /tmp/patchd/deploy/podman/preparar-vm.sh            # -u patchd -d /srv/patchd -p 8443
```

Para redirecionar a porta 443, escolha `--443` na chamada acima. Use então a URL
sem `:8443` em `PATCHD_PUBLIC_URL` e nos clientes. O script abre a HTTPS na zona
padrão do firewalld, quando ativo; ajuste a regra às redes autorizadas.

O que ele faz, para quem preferir à mão:

```sh
dnf install -y podman git openssl acl passt policycoreutils-python-utils
useradd -m patchd                         # o usuário do serviço; o useradd já reserva os subuid/subgid
grep patchd /etc/subuid /etc/subgid       # uma faixa de 65536 em cada
loginctl enable-linger patchd             # serviços do usuário no boot, sem login
mkdir -p /var/log/journal && systemd-tmpfiles --create --prefix /var/log/journal && systemctl restart systemd-journald
#   armazenamento fora do /home: seção "Disco", abaixo
firewall-cmd --permanent --add-port=8443/tcp   # HTTPS: painel e agentes
firewall-cmd --permanent --add-port=8080/tcp   # HTTP de transição e cache (só se usar)
firewall-cmd --permanent --add-forward-port=port=443:proto=tcp:toport=8443   # opcional: URL sem a porta
firewall-cmd --reload
ss -ltnp | grep -E ':(8443|8080|9090) '   # vazio: livres
```

O nome `patchd` do usuário é um exemplo; o resto do guia chama de "usuário do serviço".

O diário em disco não é enfeite: só em memória (o padrão de algumas VMs), ele não separa
por usuário, o `journalctl --user` responde "insufficient permissions", e os logs que os
scripts mostram quando algo falha somem.

### Disco

O Podman sem root guarda as imagens, o cache do build e os volumes (o banco também) em
`~/.local/share/containers`, no `/home`. Numa VM com partições separadas, o `/home` costuma ser
pequeno: só a primeira subida usa de 4 a 5 GB (a imagem do Go e o cache do build; a do
Postgres duas vezes, por causa do keep-id), e o banco cresce. O `instalar.sh` avisa abaixo de
15 GB livres. Antes do primeiro `podman` do usuário do serviço, mude para uma partição com
espaço (o `preparar-vm.sh` usa `/srv/patchd`, que fica no `/`):

```sh
# como root
install -d -m 700 -o patchd -g patchd /srv/patchd /srv/patchd/containers
semanage fcontext -a -e /home/patchd/.local/share/containers /srv/patchd/containers
restorecon -R /srv/patchd/containers
# como o usuário do serviço
mkdir -p ~/.config/containers
printf '[storage]\ndriver = "overlay"\ngraphroot = "/srv/patchd/containers/storage"\n' > ~/.config/containers/storage.conf
podman info --format '{{.Store.GraphRoot}}'    # /srv/patchd/containers/storage
```

O `semanage` dá à pasta nova os rótulos do SELinux da padrão (o tipo da pasta `storage` fica
`data_home_t`); sem ele, os containers não leem as camadas. Se o usuário já rodou algum
`podman`, `podman system reset` antes de conferir. Depois do banco criado, mudar a pasta
exige migrar os volumes: decida antes. Para produção, o melhor é um volume lógico só do
patchd, montado na mesma pasta.
A porta das métricas (9090) fica só no próprio host; para um Prometheus de outra máquina,
libere só o IP dele (regra rica do firewalld), não a porta para a rede inteira.

**Proxy de saída**, se a rede exigir: o usuário do serviço precisa dele para baixar as imagens
e os módulos do Go no build. No `~/.bashrc` do usuário do serviço:

```sh
export HTTPS_PROXY=http://PROXY:PORTA HTTP_PROXY=http://PROXY:PORTA NO_PROXY=localhost,127.0.0.1,.DOMINIO_INTERNO
```

O servidor, para buscar os catálogos, recebe o proxy pelo `deploy/.env` (`HTTPS_PROXY`,
`NO_PROXY`; o `NO_PROXY` com os nomes internos: AD, RHBK, SMTP). Proxy com inspeção TLS: a CA
dele, em PEM, em `.docker/certs/` (o build confia nela e a leva para a imagem).

**Registro de imagens**: o build baixa `docker.io/library/golang` e o pod,
`docker.io/library/postgres`. Se o Docker Hub estiver bloqueado, peça à infra o espelho
interno e configure-o em `/etc/containers/registries.conf.d/` (um `[[registry]]` com
`location = "docker.io"` e o `[[registry.mirror]]` do espelho).

## Instalação (como o usuário do serviço)

Entre com `sudo -iu patchd` ou `machinectl shell patchd@` (sessão completa; pacote
`systemd-container`). O `sudo -iu` não abre sessão: sem `XDG_RUNTIME_DIR`, o `systemctl --user`
e o `journalctl --user` digitados à mão dizem "Failed to connect to user scope bus". Os
scripts do `deploy/` ajustam sozinhos; para o resto, o `preparar-vm.sh` (desde a Aula 23.4)
grava no `~/.bash_profile` do usuário do serviço:

```sh
export XDG_RUNTIME_DIR=/run/user/$(id -u)
```

```sh
git clone https://github.com/tasantiago/patchd.git patchd && cd patchd
ls deploy/podman
cp deploy/.env.exemplo deploy/.env && vi deploy/.env  # PATCHD_PUBLIC_URL, no mínimo; 9090 ocupada: PATCHD_METRICS_PORT=9091
./deploy/podman/instalar.sh     # unidades do Quadlet, PATCHD_MOTOR=podman, imagem do Postgres
./deploy/gerar-segredos.sh      # banco, token das métricas; diz o que falta
#   TLS: server.crt e server.key em deploy/tls (da CA interna)
./deploy/atualizar.sh vX.Y.Z    # constrói, sobe o banco e o servidor e confere
systemctl --user enable --now patchd-backup.timer
```

O usuário do serviço não tem senha. Transfira certificados e outros arquivos pelo
canal administrativo autorizado e entregue-os a esse usuário, com as permissões
necessárias. Os nomes e a geração do CSR estão no README; para um laboratório sem
PKI, use [TLS de laboratório](tls-laboratorio.md), a partir de `~/patchd`.

Na primeira subida de uma frota nova, o `atualizar.sh` constrói a imagem e para antes de
subir, pedindo a chave de jobs: ele mostra o `podman run ... job keygen` com a imagem recém
construída (não há Go na VM). Gere, guarde a pública para o build dos agentes e rode o
`atualizar.sh` de novo; a construção sai do cache. Com o banco vazio, não há cópia antes da
troca (nem volta).

Mudou o `deploy/.env` (portas, LDAP, SMTP...)? `./deploy/podman/instalar.sh` de novo e
`systemctl --user restart patchd-server`. Portas: `systemctl --user restart patchd-pod` e
`systemctl --user start patchd-server` (é o pod que publica). Mudou a versão?
`./deploy/atualizar.sh`, como no compose. Os parâmetros que também mudam em Configuração ›
Parâmetros (título, fuso, retenção, notificações, grupos) seguem o que a tela gravou: o `.env`
só vale de novo com "restaurar o padrão".

## Login pelo AD (LDAPS)

O patchd fala com o AD só por LDAPS (porta 636): a senha da rede não passa aberta. No
`deploy/.env`, o controlador de domínio, o domínio e os grupos (a base de busca pode ficar
vazia: sai do domínio), e então `./deploy/podman/instalar.sh` e
`systemctl --user restart patchd-server`.

**A CA da instituição na imagem.** O certificado do DC é emitido pela CA interna, e a imagem
do servidor só confia no que estiver em `.docker/certs/` na hora do build (fora do git; é a
mesma pasta da CA de um proxy com inspeção TLS). Para exportar a cadeia exata que assina o
DC, num Windows do domínio, no PowerShell:

```powershell
$dc = 'NOME_DO_DC'
$c = New-Object Net.Sockets.TcpClient($dc, 636)
$s = New-Object Net.Security.SslStream($c.GetStream(), $false, { $true }); $s.AuthenticateAsClient($dc)
$ch = New-Object Security.Cryptography.X509Certificates.X509Chain
[void]$ch.Build((New-Object Security.Cryptography.X509Certificates.X509Certificate2($s.RemoteCertificate)))
$i = 0
foreach ($e in ($ch.ChainElements | Select-Object -Skip 1)) {
  $i++; $p = "$env:USERPROFILE\Desktop\ca-ad-$i.crt"
  "-----BEGIN CERTIFICATE-----`n" + [Convert]::ToBase64String($e.Certificate.RawData, 'InsertLineBreaks') + "`n-----END CERTIFICATE-----" | Set-Content -Encoding ascii $p
  "$p"
}
$c.Close()
```

Saem a intermediária e a raiz (`ca-ad-1.crt`, `ca-ad-2.crt`). Na VM, como o usuário do
serviço (os arquivos chegam por `/tmp`, como os outros):

```sh
mkdir -p ~/patchd/.docker/certs && cp /tmp/ca-ad-*.crt ~/patchd/.docker/certs/
cat ~/patchd/.docker/certs/*.crt > /tmp/cadeia.pem
openssl s_client -connect NOME_DO_DC:636 -brief -tls1_2 -CAfile /tmp/cadeia.pem </dev/null 2>&1 | grep -E 'Protocol|Verification'
#   esperado: Protocol version: TLSv1.2 e Verification: OK
./deploy/atualizar.sh vX.Y.Z     # nova versão: a imagem é reconstruída com a CA ("2 added" no update-ca-certificates)
read -rsp 'Senha: ' S; echo; printf '%s\n' "$S" | podman exec -i patchd-server /patchd-server ad check -user SEU_USUARIO; unset S
```

O `ad check` mostra cada etapa (LDAPS, senha, grupos, perfil) sem abrir sessão. Depois:

- A **conta local** de administrador continua sendo a contingência (AD ou 636 fora do ar):
  senha num cofre, sem uso no dia a dia.
- Sem `PATCHD_LDAP_READ_GROUP`, só o grupo admin entra; a equipe que só consulta precisa de um
  grupo de leitura (no `.env` ou em Configuração › Acesso).
- Os `.crt` ficam só nesta VM: numa reinstalação, eles vão junto. Quando a CA da instituição
  for renovada, exporte de novo e rode o `atualizar.sh` (até lá, o `ad check` diz "CA
  desconhecida").
- O `agente.sh` usa a mesma pasta no build do agente.

## Operação

| No compose | No Podman |
|---|---|
| `./deploy/compose.sh ps` | `systemctl --user status patchd-server patchd-postgres` |
| `./deploy/compose.sh logs --tail 50 server` | `journalctl --user -u patchd-server -n 50` |
| `./deploy/compose.sh exec server /patchd-server ARGS` | `podman exec patchd-server /patchd-server ARGS` |
| `./deploy/compose.sh restart server` | `systemctl --user restart patchd-server` |
| `./deploy/compose.sh up -d` | `systemctl --user start patchd-server` |
| `docker image ls patchd-server` | `podman image ls patchd-server` |
| timer `patchd-backup` do sistema (`deploy/systemd`) | timer `patchd-backup` do usuário (`systemctl --user list-timers`) |
| `patchd-server package fetch` (ainda vale) | botão Baixar na página do programa (Aula 23.3, `docs/operacao.md`) |

A versão no ar é a imagem `localhost/patchd-server:atual`; o `atualizar.sh` troca essa tag
e as versões ficam como `localhost/patchd-server:vX.Y.Z`, para a volta (`--voltar`).

## Agente: compilar, assinar e publicar pelo servidor (Aula 23.2)

O painel não compila o agente: ele distribui a versão publicada na pasta `releases/`
(Configuração › Agente, com o token e o comando de instalação). O `deploy/agente.sh` faz a
versão ali mesmo, sem o ambiente de desenvolvimento e sem Go no host:

### Primeira frota: criar a chave de releases na administração

Em uma **estação Linux administrativa com Git, Bash e Podman**, use um clone
separado do mesmo código aprovado para a instalação. A privada fica nessa estação,
fora do repositório, com backup protegido offline. Não reutilize a chave de jobs.

```bash
git clone https://github.com/tasantiago/patchd.git patchd-assinatura
cd patchd-assinatura
# Substitua pelo hash registrado na implantação do servidor.
git checkout --detach COMMIT_REGISTRADO_NO_SERVIDOR
PATCHD_GO_IMAGE="$(sed -n 's/^FROM \(docker\.io\/library\/golang:[^ ]*\).*/\1/p' deploy/Dockerfile | head -n 1)"
install -d -m 700 "$HOME/.config/patchd-signing"
podman run --rm --userns keep-id --user "$(id -u):$(id -g)" \
  -v "$PWD:/src:ro,Z" \
  -v "$HOME/.config/patchd-signing:/chaves:Z" \
  -e GOCACHE=/tmp/go-build -e GOMODCACHE=/tmp/go-mod -e GOTELEMETRY=off \
  --workdir /src "$PATCHD_GO_IMAGE" bash -euc \
  'go run ./cmd/release keygen -key /chaves/release.key > /chaves/release.pub'
```

`COMMIT_REGISTRADO_NO_SERVIDOR` é um campo a substituir. O comando baixa os
módulos, gera a privada `release.key` e grava somente a pública em `release.pub`.
A criação recusa sobrescrever uma privada existente; nos próximos builds, use a
mesma chave, seguindo o plano de rotação quando necessário. Configure proxy e
confiança nas CAs públicas da rede nessa estação, se exigidos.

### Compilar, assinar e publicar

Do seu computador administrativo, transfira a chave somente para a memória do
servidor. Substitua os nomes da conta administrativa e do servidor:

```sh
# no seu computador, onde a chave de release fica guardada:
scp "$HOME/.config/patchd-signing/release.key" SEU_USUARIO@NOME_DO_SERVIDOR:/dev/shm/release.key
# no servidor, como root:
chown patchd /dev/shm/release.key
chmod 600 /dev/shm/release.key
# como o usuário do serviço, na pasta do repositório:
cd ~/patchd
# Use a versão real da implantação; este é o rótulo inicial do README.
./deploy/agente.sh v0.0.0-instalacao.1 -key /dev/shm/release.key
```

1. Confere: árvore limpa, servidor no ar, versão não mais nova que a do servidor (atualize o
   servidor antes, com o `atualizar.sh`), versão ainda não assinada.
2. Pega a pública de jobs do servidor no ar (`patchd-server job pubkey`) e a de release da
   própria chave; as seguintes e a organização, do `deploy/.env`.
3. Num clone limpo do commit, num container da mesma imagem do Go do `deploy/Dockerfile`:
   `scripts/build.sh` e `patchd-release sign`. As caches do Go ficam nos volumes
   `patchd-gomod` e `patchd-gocache` (a segunda vez é bem mais rápida).
4. Copia a versão assinada para `releases/` e publica (`release publish`). Os agentes
   instalados a recebem no próximo check-in; o painel passa a oferecê-la para download.

**A chave de release não mora no servidor.** Quem a tivesse aqui assinaria um "agente" para
a frota inteira, passando por cima de toda conferência dos jobs. Em `/dev/shm` ela fica só na
memória; o script a passa ao container pela entrada padrão (num tmpfs, nunca em disco) e a
apaga no fim, mesmo se o build falhar. Fora de `/dev/shm`, o script assina, mas avisa.
Se ele recusar a execução nas verificações iniciais, antes de começar o build,
remova manualmente a cópia temporária da chave de `/dev/shm`. O uso de memória não
dispensa as proteções e a política de swap da VM.

Voltar a oferecer uma versão anterior que ainda está em `releases/`:
`./deploy/agente.sh --publicar vX.Y.Z` (os agentes não voltam de versão sozinhos; isso só
muda o que o painel e as máquinas novas recebem). Retirar a oferta, sem versão nenhuma:
`rm releases/current` (o servidor no ar só lê a pasta).

## Problemas comuns

- **"short-name resolution enforced but cannot prompt without a TTY"**: uma imagem sem o
  registro no nome. As do repositório já vêm com `docker.io/library/...`.
- **`Failed to connect to bus`** no `systemctl --user`: sessão sem `XDG_RUNTIME_DIR`. Use
  `sudo -iu USUARIO` e os scripts do `deploy/`, ou `export XDG_RUNTIME_DIR=/run/user/$(id -u)`.
- **Serviços param quando o usuário sai** ou não sobem no boot: falta o linger
  (`loginctl enable-linger USUARIO`, como root).
- **Porta já em uso** (o pod não sobe; o log fala em `Failed to bind port`, e o
  `atualizar.sh`, em "o Postgres não subiu"): outra coisa no host usa a porta. No RHEL, a 9090
  costuma ser o **Cockpit** (`ss -ltnp` mostra `systemd`, que ativa o `cockpit.socket`): use
  `PATCHD_METRICS_PORT=9091`. Mude a porta no `.env` e rode o `instalar.sh`, que confere as
  portas antes de gerar as unidades. Com o pod no ar, o `ss` mostra as portas dele: é normal.
- **`Start request repeated too quickly`**: depois de algumas falhas seguidas, o systemd para
  de tentar. Os scripts do `deploy/` zeram a contagem antes de subir; à mão,
  `systemctl --user reset-failed patchd-pod patchd-postgres patchd-server`.
- **`No journal files were opened due to insufficient permissions`** no `journalctl --user`:
  o diário está só em memória. Como root, ponha-o em disco (seção "Preparação"); até lá,
  como root, `journalctl _UID=$(id -u patchd) -n 60`.
- **Permissão negada lendo um segredo**: confira o dono (o usuário do serviço) e o SELinux
  (`ausearch -m avc -ts recent`, como root). Os `:Z` das unidades rotulam a cada subida.
- **O servidor não fica saudável**: `journalctl --user -u patchd-server -n 50`. O
  `atualizar.sh` para, mostra o fim do log e diz o comando da volta.
- **Root não vê nada**: `podman ps` como root olha outro armazenamento. Tudo do patchd é do
  usuário do serviço. E o `podman pod ps` não tem `-a`: ele já lista todos.
- **Login no painel recusado como "conta local recusada"**, com o AD configurado: o servidor
  ainda roda sem o AD (o `restart` não aconteceu, ou o `instalar.sh` não rodou depois do
  `.env`). `grep PATCHD_LDAP_HOST ~/.config/patchd/servidor.env` e `systemctl --user restart
  patchd-server`.
- **`ad check`: "connection reset by peer"** na conexão LDAPS: a conexão abre e cai no
  primeiro pacote do TLS. Separe as causas:
  1. da VM, `openssl s_client -connect NOME_DO_DC:636 -brief -tls1_2 </dev/null`: `errno=104`
     confirma que é antes do patchd;
  2. de um Windows do domínio, o teste do PowerShell da seção do AD (só as quatro primeiras
     linhas): passa com `Tls12`? Então o DC tem certificado e fala TLS 1.2;
  3. quem manda o reset, como root:
     `tcpdump -ni any -c 20 -w /tmp/ldaps.pcap 'port 636' &`, o `openssl` do passo 1, e
     `tcpdump -nr /tmp/ldaps.pcap -v | grep -oE 'ttl [0-9]+|Flags \[[^]]*\]' | paste - -`.
     O `[S.]` do DC (Windows) vem com TTL perto de 128; um `[R.]` com outro TTL (63, por
     exemplo) foi injetado por um firewall ou IPS no caminho: peça a liberação da 636 da
     VM para os DCs (a 389 aberta não basta).
- **`ad check`: "certificado: emitido por CA desconhecida"**: falta a CA da instituição na
  imagem (seção do AD).
- **`gerar-segredos.sh` falando em `go run` e `setfacl`**: ele rodou antes do `instalar.sh` (que
  anota `PATCHD_MOTOR=podman`). Desde a 23.1b ele para e avisa; se já tiver rodado, nada se
  perde: `setfacl -b deploy/secrets/metrics_token` e siga.

