# Portas, comunicação e horário

Alterar porta, URL publicada ou fuso é uma mudança de configuração. Use a imagem já instalada; mantenha `PATCHD_VERSION` e `PATCHD_COMMIT`. Para obter uma funcionalidade que não existe nessa imagem, siga a [atualização de versão](../README.md#gerar-e-publicar-uma-nova-versão-do-servidor-e-do-agente).

## Qual porta atende cada conexão?

| Finalidade | Configuração | Comportamento |
| --- | --- | --- |
| HTTPS principal | `PATCHD_TLS_PORT` no host; listener interno `:8443` no deploy fornecido | Painel, API, enrollment e comunicação normal dos agentes. |
| URL pública principal | `PATCHD_PUBLIC_URL=https://NOME:PORTA` | Endereço de referência publicado pelo servidor; não reconfigura sozinho agentes existentes. |
| HTTPS só de agentes | `PATCHD_AGENT_LISTEN_ADDR`, `PATCHD_AGENT_BIND`, `PATCHD_AGENT_PORT` | Opcional e desligado por padrão; recebe `/api/v1/agent/...` e `/healthz`. Recusa painel, API de leitura e enrollment. O check-in é marcado como vindo pelo relé. |
| HTTP de transição/cache | `PATCHD_HTTP_LISTEN_ADDR`, `PATCHD_HTTP_PORT`, `PATCHD_HTTP_CACHE_ONLY` | Fluxo de transição e cache; não substitui a configuração HTTPS. O roteiro de instalação nova desliga o listener HTTP. |
| Métricas | `PATCHD_METRICS_LISTEN_ADDR`, `PATCHD_METRICS_PORT`, `PATCHD_METRICS_BIND` | Endpoint próprio com TLS e token. Não é a porta dos agentes. |
| Relé público | **Relé para fora da rede**, parâmetro `PATCHD_RELAY_ADDR=HOST:PORTA` | Endereço discado pelo agente compatível quando o caminho direto falha; veja [Relé](rele.md). |

O canal em tempo real usa a mesma porta de comunicação do agente. Não é necessário abrir uma porta de entrada nas estações. A porta só de agentes existe nas revisões atuais: instruções anteriores à sua implementação que dizem haver somente a porta compartilhada não descrevem essa opção nova.

## Alterar a HTTPS principal sem nova versão

Na pasta do projeto, ajuste as linhas existentes de `deploy/.env`, sem duplicá-las:

```dotenv
PATCHD_TLS_PORT=8443
PATCHD_PUBLIC_URL=https://patchd.exemplo.invalid:8443
```

Substitua o nome ilustrativo pelo DNS coberto pelo certificado. Confira a disponibilidade da porta e atualize as regras de rede da instalação.

**Podman**, como o usuário do serviço `patchd`:

```bash
./deploy/podman/instalar.sh
systemctl --user restart patchd-pod
systemctl --user start patchd-server
systemctl --user status patchd-server --no-pager
```

O `instalar.sh` regenera Quadlets e ambiente e recarrega o systemd; não compila outra imagem. A publicação das portas pertence ao pod, por isso reiniciar só o servidor não aplica a troca. O reinício do pod também interrompe temporariamente o banco e as conexões.

**Docker Compose**:

```bash
./deploy/compose.sh up -d --no-build --pull never --force-recreate server
./deploy/compose.sh ps
```

Esse comando usa a imagem local da versão configurada. Um simples `restart server` não aplica mudanças de ambiente ou de publicação de portas. Se a imagem dessa versão não existir no host, o comando falha; confira a versão configurada, sem criar um rótulo novo para contornar o erro.

Valide pela rede dos agentes, com a CA confiável:

```bash
curl --fail --show-error --cacert deploy/tls/ca.crt \
  https://patchd.exemplo.invalid:8443/healthz
```

Use a CA real da instalação; se ela já estiver na confiança do sistema, `--cacert` é opcional. Configure [a porta só de agentes](rele.md#3-a-porta-só-de-agentes-no-patchd) separadamente se essa for a finalidade da mudança.

## Migrar agentes existentes pelo servidor

Mantenha o endereço antigo acessível até os agentes aprenderem o novo. Primeiro disponibilize e teste a nova entrada para o **mesmo servidor e banco**, com TLS válido; só depois envie `server_url`. Se a porta antiga já foi desligada, um agente que só conhece aquela porta não consegue buscar o job. Restaure temporariamente o caminho antigo ou faça a correção local descrita abaixo.

Exemplo Podman para um grupo de implantação:

```bash
podman exec patchd-server /patchd-server ring list
podman exec patchd-server /patchd-server job create \
  -ring 1 -type server_url \
  -url https://patchd.exemplo.invalid:8443 -ttl 168h
```

`-ring 1` inclui só as máquinas ativas daquele grupo. O comando não possui `-all`: para todos os grupos ocupados, execute em Bash, como o usuário do serviço:

```bash
migrar_agentes() {
  local nova_url='https://patchd.exemplo.invalid:8443'
  local lista anel
  lista=$(podman exec patchd-server /patchd-server ring list) || return
  while IFS= read -r anel; do
    podman exec patchd-server /patchd-server job create \
      -ring "$anel" -type server_url -url "$nova_url" -ttl 168h || return
  done < <(printf '%s\n' "$lista" | awk '
    $1 ~ /^[0-9]$/ {
      n = ($2 == "(piloto)" ? $3 : $2)
      if (n ~ /^[0-9]+$/ && n > 0) print $1
    }
  ')
}
migrar_agentes
```

Substitua a URL antes de executar. No Docker, use `./deploy/compose.sh exec -T server /patchd-server` no lugar de `podman exec patchd-server /patchd-server`. A seleção contempla as máquinas cadastradas naquele momento. Agentes desligados precisam voltar dentro das 168 horas; depois disso os jobs expiram. Evite executar o bloco repetidamente sem conferir os jobs já criados.

```bash
podman exec patchd-server /patchd-server job list -n 100
```

O agente recebe o job pelo canal/check-in atual, testa o endereço novo e só grava a troca depois da resposta aceita por HTTPS. Uma falha mantém o endereço anterior e registra o motivo; não há nova tentativa automática desse job. Acompanhe **Automação › Jobs**, o último contato e os logs antes de fechar a entrada antiga.

## Corrigir o endereço diretamente no agente

Para uma máquina que já não alcança a entrada antiga, pare o serviço, faça backup protegido do `credential.json` **na própria pasta de dados** e altere somente a URL do mesmo servidor. No Windows, o padrão é `C:\ProgramData\patchd\credential.json`; se instalou com `-data-dir`, use essa pasta.

```powershell
Stop-Service patchd-agent
Copy-Item 'C:\ProgramData\patchd\credential.json' 'C:\ProgramData\patchd\credential.json.bak'
```

No JSON, coloque o novo endereço em `server_url` e o endereço que estava em uso em `previous_server_url`. Preserve `machine_id`, `credential` e os demais campos. Salve JSON válido como UTF-8 sem BOM e com as permissões protegidas originais; não copie o conteúdo para chamados ou para o repositório.

```powershell
Start-Service patchd-agent
Get-Service patchd-agent
```

Isso preserva o cadastro; não remova a credencial para registrar outra vez. Só alterar `-server` ou `PATCHD_SERVER_URL` não substitui a URL de um agente já registrado: ele usa a URL guardada na credencial e confere a compatibilidade com a configuração de início.

## Relógio, fuso do painel e fuso dos logs

O contêiner normalmente compartilha o relógio do host. **Fuso muda a representação do instante**: `13:47:45-04:00` e `17:47:45Z` são a mesma hora. `Z` e o cabeçalho HTTP `Date` usam UTC; diferença de quatro horas para Manaus não prova desvio do relógio.

| O que quero mudar/ver | Procedimento |
| --- | --- |
| Horas exibidas no painel e agendas | **Configuração › Parâmetros › Aparência › Fuso horário**, por exemplo `America/Manaus`. `PATCHD_TIMEZONE` é o valor inicial; o parâmetro salvo no painel prevalece. |
| Fuso local do processo e dos novos logs no Podman | Configure `Timezone=America/Manaus` na seção `[Container]` do modelo `deploy/podman/patchd-server.container`; regenere as unidades e reinicie o servidor. |
| Fuso local do processo no Docker | Monte, como somente leitura, o arquivo de zona do host em `/etc/localtime`, conforme o exemplo abaixo, e recrie o servidor com a mesma imagem. |
| Hora real e sincronização | No host: `date`, `date -u` e `timedatectl status`. Corrija a sincronização no host se houver desvio real. |

Podman, depois de acrescentar `Timezone=America/Manaus` no modelo:

```bash
./deploy/podman/instalar.sh
systemctl --user restart patchd-server
podman logs --since 1m --timestamps patchd-server
```

Docker: acrescente na lista `services.server.volumes` de `deploy/compose.yaml`, preservando as outras montagens:

```yaml
- /usr/share/zoneinfo/America/Manaus:/etc/localtime:ro
```

Depois execute o `up -d --no-build --pull never --force-recreate server` pelo wrapper. Confirme que o arquivo de zona existe no host. Preserve a mudança local de configuração ao atualizar o checkout; não precisa gerar uma imagem por causa desse ajuste.

A imagem de produção é `scratch`: não possui shell nem `date`, e não inclui a árvore de zonas do sistema. Por isso, acrescentar apenas `TZ=America/Manaus` não é um procedimento garantido para ela. O Quadlet `Timezone=` ou a montagem de `/etc/localtime` fornece a zona ao processo. O campo `time` dos novos logs deve adotar o deslocamento; logs antigos não são reescritos. Confira também o fuso escolhido para visualizar o prefixo acrescentado pelo motor de contêineres.
