# Dados de teste

Respostas da API do Bodhi (https://bodhi.fedoraproject.org), reduzidas aos campos que o patchd lê.

- `releases-current.json`: a lista de versões em suporte, com os nomes, prefixos e dist_tags vistos na
  exploração da Aula 6.2 (parte 1), resumida a sete itens.
- `updates-F44.json`: o primeiro update (`FEDORA-2026-35b989661c`, chromium) traz os valores reais da
  exploração, com os títulos dos bugs cortados com `...`. Os outros dois são construídos para cobrir os
  casos de borda: época 1 e época null, bug que não é de segurança, build que não é rpm, NVR fora do
  formato e datas null.
