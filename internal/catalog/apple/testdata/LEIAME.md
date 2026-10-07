# Dados de teste

Recortes de respostas reais, de 5 de outubro de 2026, reduzidos para caber no repositório:

- `gdmf-pmv-recorte.json`: resposta do `https://gdmf.apple.com/v2/pmv` (Apple). As versões do macOS
  estão completas; a lista de modelos de cada uma foi cortada, e iOS e visionOS foram reduzidos.
- `sofa-macos-v2-recorte.json`: o feed v2 do SOFA (`https://sofafeed.macadmins.io/v2/macos_data_feed.json`,
  projeto Mac Admins, licença Apache 2.0), com quatro majors, algumas versões de cada e até três CVEs por
  versão (mais as exploradas). As contagens (`UniqueCVEsCount`) são as originais. A seção `Models` tem sete
  modelos reais (Aula 6.4, parte 4a), um de cada situação: aceitam até o 27, o 26, o 15 e o 14.
