# TLS de laboratório para uma instalação nova

Este procedimento cria uma CA própria para um laboratório isolado. Para produção,
solicite o certificado à PKI da organização e siga sua política de emissão e renovação.

Pré-requisitos: OpenSSL, terminal Bash na VM e `deploy/tls/server.key` e
`server.csr` gerados no [passo de TLS do Docker](../README.md#5-preparar-tls) ou
[do Podman](../README.md#p4-emitir-tls-e-preparar-os-segredos). Execute como o mesmo
usuário que preparou os arquivos: root no roteiro Docker, `patchd` no Podman.
Defina `PATCHD_FQDN` com o mesmo nome usado em
`PATCHD_PUBLIC_URL`; o DNS ou a resolução controlada do laboratório deve apontar
esse nome para a VM.

Entre na raiz do clone (`/opt/patchd` no Docker, `~/patchd` no Podman). A privada
da CA será criada fora do repositório, em `$HOME/.local/share/patchd-ca-lab`. Use
uma pasta nova: o bloco recusa uma CA ou certificado de servidor já existentes.

```bash
(
  set -euo pipefail
  : "${PATCHD_FQDN:?defina o nome DNS do servidor}"
  PATCHD_CA_DIR="$HOME/.local/share/patchd-ca-lab"
  install -d -m 700 "$PATCHD_CA_DIR"
  test ! -e "$PATCHD_CA_DIR/ca.key"
  test ! -e "$PATCHD_CA_DIR/ca.crt"
  test ! -e deploy/tls/server.crt
  test -s deploy/tls/server.csr
  umask 077

  openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 365 \
    -keyout "$PATCHD_CA_DIR/ca.key" \
    -out "$PATCHD_CA_DIR/ca.crt" \
    -subj '/CN=patchd-laboratorio-ca' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign'

  cat > "$PATCHD_CA_DIR/server.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${PATCHD_FQDN}
EOF

  openssl x509 -req -in deploy/tls/server.csr -sha256 -days 30 \
    -CA "$PATCHD_CA_DIR/ca.crt" -CAkey "$PATCHD_CA_DIR/ca.key" \
    -CAcreateserial -CAserial "$PATCHD_CA_DIR/ca.srl" \
    -extfile "$PATCHD_CA_DIR/server.ext" -out deploy/tls/server.crt

  cp "$PATCHD_CA_DIR/ca.crt" deploy/tls/ca.crt
  openssl verify -CAfile deploy/tls/ca.crt \
    -verify_hostname "$PATCHD_FQDN" deploy/tls/server.crt
)
```

O resultado esperado é `deploy/tls/server.crt: OK`. O certificado do servidor vale
30 dias; planeje renovação antes do vencimento. Volte ao roteiro escolhido no
README: no Docker, prepare os segredos e as ACLs do passo 6; no Podman, prossiga
no passo P4 como o usuário do serviço, sem ACLs. Depois suba os serviços e valide
`/healthz` com `curl --cacert`.

Distribua **somente `deploy/tls/ca.crt`**, a parte pública, aos clientes de teste
que precisam confiar na CA. Não distribua `ca.key` ou `server.key`. Preserve a
privada da CA em armazenamento administrativo protegido fora da VM da aplicação;
ela permite emitir certificados aceitos por todos os clientes que confiam nela.

O healthcheck interno do contêiner comprova disponibilidade local. A chamada do
README com `curl --cacert` também verifica a identidade e a confiança no
certificado; um serviço saudável não substitui essa validação externa.

