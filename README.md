# patchd

Mini-ITOM de coleta e gestão de patches para Windows, Linux e macOS, escrito em Go.

- **patchd-agent**: binário único que coleta inventário, escaneia patches e executa jobs. Roda como serviço Windows, unidade systemd ou LaunchDaemon.
- **patchd-server**: API REST, catálogo de vulnerabilidades, motor de compliance, fila de jobs e painel web, em container com PostgreSQL.

Princípio: o agente coleta e executa; o servidor decide.

## Desenvolvimento

O toolchain roda em container (Go 1.27).

```bash
printf 'UID=%s\nGID=%s\n' "$(id -u)" "$(id -g)" > .env
docker compose up -d --build
docker compose exec dev zsh
go build -o bin/ ./cmd/...
```

Certificados de CA corporativa (opcionais) vão em `.docker/certs/*.crt` e não são versionados.

## Documentação

- [Requisitos](docs/requisitos.md)
- [Fontes de verdade por SO](docs/fontes-de-verdade.md)
