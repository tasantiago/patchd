# Imagem de PRODUÇÃO do patchd-server.
# Build manual, a partir da raiz do repositório:
#   docker build -f deploy/Dockerfile --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse HEAD) -t patchd-server:v0.1.0 .

# ---------- Estágio 1: compilação ----------
FROM golang:1.27.1-trixie AS build

# CA corporativa (opcional): necessária para baixar módulos atrás da inspeção TLS
# e copiada para a imagem final, para o servidor buscar os catálogos.
COPY .docker/certs/ /usr/local/share/ca-certificates/
RUN update-ca-certificates

WORKDIR /src

# Dependências primeiro: esta camada só é refeita quando go.mod/go.sum mudam.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# Sem a pasta .git no contexto, versão e commit chegam por argumento de build.
ARG VERSION=dev
ARG COMMIT=
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w \
        -X github.com/tasantiago/patchd/internal/buildinfo.Version=${VERSION} \
        -X github.com/tasantiago/patchd/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/patchd-server ./cmd/server

# ---------- Estágio 2: imagem final ----------
# scratch: imagem vazia. Sem shell, sem gerenciador de pacotes, só o binário e as CAs.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/patchd-server /patchd-server

# Usuário sem privilégios (não existe em /etc/passwd; o número basta).
USER 65532:65532

EXPOSE 8080

# Sem curl na imagem: o próprio binário verifica o /healthz.
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/patchd-server", "-healthcheck"]

ENTRYPOINT ["/patchd-server"]
