# Build e publicação de agentes sem o contêiner de desenvolvimento

O servidor do [README](../README.md#instalar-um-servidor-novo-do-zero) é compilado
pelo Dockerfile de produção. Para uma frota nova, os agentes também podem sair
diretamente do Git e de uma imagem oficial de Go. O fluxo abaixo usa Docker numa
**estação Linux de build/administração confiável, com Bash**, separada do servidor da aplicação.

Você precisa de Git e Docker nessa estação, do commit usado no servidor e da
**pública** de jobs gerada na instalação. A privada de jobs permanece no servidor.
A privada de releases será criada e guardada nesta estação, com cópia protegida
offline; ela não precisa ir para a VM do servidor.

## Atualizações de uma frota existente

Para gerar a próxima versão, siga o [roteiro de atualização do README](../README.md#gerar-e-publicar-uma-nova-versão-do-servidor-e-do-agente).
Ele atualiza o servidor primeiro e mostra o build/publicação com Docker ou Podman.
**Não repita a criação da chave do passo 2**: reutilize a privada de releases que
corresponde à pública confiada pelos agentes instalados e preserve a chave de jobs.
Escolha um número novo, maior que o instalado; os rótulos `.1` abaixo são exemplos
para a primeira instalação, não valores a reutilizar a cada publicação.

## 1. Obter uma cópia limpa do mesmo código

```bash
git clone https://github.com/tasantiago/patchd.git patchd-build
cd patchd-build

# Substitua pelo commit registrado na implantação do servidor.
git checkout --detach COMMIT_REGISTRADO_NO_SERVIDOR
git status --short

# Use a mesma versão escolhida no README para o primeiro build da instalação.
export PATCHD_AGENT_VERSION='v0.0.0-instalacao.1'
export PATCHD_JOB_PUBKEY='COLE_A_PUBLICA_BASE64_DE_JOBS_DESTA_INSTALACAO'
PATCHD_GO_IMAGE="$(sed -n 's/^FROM \(docker\.io\/library\/golang:[^ ]*\).*/\1/p' deploy/Dockerfile | head -n 1)"
```

As duas entradas em maiúsculas são campos a substituir, não valores executáveis
de exemplo. `git status --short` deve estar vazio. O número do agente não pode
ser mais novo que o do servidor. Os scripts de release conferem o formato da
versão, as públicas e a árvore limpa antes de compilar.

## 2. Criar a chave de assinatura de releases

Crie o diretório fora do clone, na estação de administração:

```bash
install -d -m 700 "$HOME/.config/patchd-signing"
docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$PWD,dst=/src,readonly" \
  --mount "type=bind,src=$HOME/.config/patchd-signing,dst=/chaves" \
  -e GOCACHE=/tmp/go-build -e GOMODCACHE=/tmp/go-mod -e GOTELEMETRY=off \
  --workdir /src "$PATCHD_GO_IMAGE" bash -euc \
  'go run ./cmd/release keygen -key /chaves/release.key > /chaves/release.pub'
```

Essa criação é feita uma única vez e recusa sobrescrever `release.key`. Para os
builds seguintes, use a mesma chave, seguindo o procedimento de rotação quando
necessário. A pública fica em `release.pub`; mantenha a privada e seu backup sob
controle administrativo.

## 3. Compilar e assinar

Na mesma estação e no mesmo clone:

```bash
mkdir -p releases
docker run --rm --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$PWD,dst=/src" \
  --mount "type=bind,src=$HOME/.config/patchd-signing,dst=/chaves,readonly" \
  -e PATCHD_AGENT_VERSION -e PATCHD_JOB_PUBKEY \
  -e GOCACHE=/tmp/go-build -e GOMODCACHE=/tmp/go-mod -e GOTELEMETRY=off \
  --workdir /src "$PATCHD_GO_IMAGE" bash -euc '
    export PATCHD_UPDATE_PUBKEY="$(go run ./cmd/release pubkey -key /chaves/release.key)"
    ./scripts/build.sh "$PATCHD_AGENT_VERSION"
    go run ./cmd/release sign -key /chaves/release.key \
      -version "$PATCHD_AGENT_VERSION" -dist dist -out releases
  '
```

O script compila agentes para Windows, Linux e macOS, nas arquiteturas amd64 e
arm64, injeta as públicas e gera o recurso de versão do Windows. A assinatura
confere os binários e cria `releases/VERSAO/`, com os executáveis, manifesto e
assinatura. Uma versão assinada existente não é sobrescrita.

As caches Go usadas nos comandos acima são temporárias dentro do contêiner. Se
houver proxy ou inspeção TLS, configure a saída e a confiança do ambiente de build
com as CAs públicas da organização antes de baixar módulos. Isso não exige
copiar arquivos de um contêiner de desenvolvimento.

## 4. Transferir somente os artefatos assinados

Na estação de build:

```bash
tar -C releases -czf "/tmp/patchd-agentes-${PATCHD_AGENT_VERSION}.tar.gz" "$PATCHD_AGENT_VERSION"
```

Transfira esse arquivo por seu canal administrativo para a VM. Não envie a
privada de releases. No terminal administrativo do servidor, extraia a pasta da
versão em `/opt/patchd/releases/`, preservando seu nome e conteúdo:

```bash
cd /opt/patchd
PATCHD_AGENT_VERSION='v0.0.0-instalacao.1'
tar -tzf "/tmp/patchd-agentes-${PATCHD_AGENT_VERSION}.tar.gz"
tar -xzf "/tmp/patchd-agentes-${PATCHD_AGENT_VERSION}.tar.gz" -C releases
```

Confira a listagem antes de extrair: ela deve conter somente o diretório da versão
e os artefatos assinados esperados. O caminho `/tmp` refere-se à cópia recebida
nesta VM, não ao arquivo que continua na estação de build.

## 5. Publicar com a imagem de produção

O servidor em execução monta as releases somente para leitura. A publicação
altera o apontador da versão oferecida, então execute-a em um contêiner temporário
com essa pasta montada para escrita:

```bash
PATCHD_INSTALACAO_VERSION='v0.0.0-instalacao.1'
find releases -type d -exec setfacl -m u:65532:rx {} +
find releases -type f -exec setfacl -m u:65532:r {} +
docker run --rm --network none --user "$(id -u):$(id -g)" \
  --mount "type=bind,src=$PWD/releases,dst=/var/lib/patchd/releases" \
  -e PATCHD_RELEASES_DIR=/var/lib/patchd/releases \
  "patchd-server:$PATCHD_INSTALACAO_VERSION" release publish "$PATCHD_AGENT_VERSION"

./deploy/compose.sh exec -T server /patchd-server release current
```

Use a versão real da imagem e do agente se tiver escolhido outros rótulos.
Espere que `release current` devolva a versão publicada. **Configuração › Agente**
passa a oferecer os arquivos dessa versão; os agentes existentes que confiam na
pública de releases a recebem pelo fluxo de atualização.

Para a primeira estação, registre um piloto com a URL HTTPS, a CA pública e um
token de enrollment. Confira o primeiro check-in e o inventário. Assinatura e
publicação corretas não substituem a homologação da instalação por plataforma.

## O que preservar

- Privada de jobs no servidor e cópia protegida para recuperação.
- Privada de releases na administração e backup offline.
- Públicas usadas no build e o commit de origem.
- Artefatos assinados publicados e o procedimento de distribuição.
- CA pública distribuída aos agentes e o plano de renovação do TLS.

Para rotação das chaves e recuperação, consulte [docs/operacao.md](operacao.md).
