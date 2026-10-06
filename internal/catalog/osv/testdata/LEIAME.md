# Dados de teste

Os arquivos `USN-*.json` são avisos reais do Ubuntu, no formato OSV, copiados sem alteração
do repositório [canonical/ubuntu-security-notices](https://github.com/canonical/ubuntu-security-notices)
(pasta `osv/usn`). Licença: [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/),
da Canonical Ltd.

- `USN-8861-1`: openssl, só no 26.04 (o caso cruzado com a VM do laboratório).
- `USN-8862-1`: libxpm, com época na versão (`1:3.5.17-…`).
- `USN-8863-1`: gst-plugins-good1.0, em seis ecossistemas, três do Ubuntu Pro, com CVEs
  diferentes por ecossistema.
