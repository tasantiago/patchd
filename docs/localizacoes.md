undefined
## Regras independentes por andar

Todos os prédios do patrimônio são apresentados, mesmo sem equipamentos ou faixas prévias. Em cada prédio, cadastre nome do andar e regex Go/RE2 para o IPv4 principal (ex.: `^10\.12\.1\.[0-9]+$`). Regras iguais em prédios diferentes são independentes. Regras com o mesmo nome agregam; nomes diferentes que correspondem ao mesmo IP deixam o andar sem atribuição. A tela mostra contagens por regra, sem correspondência e conflitos. As contagens por regra podem se sobrepor. Faixas antigas continuam como fallback quando nenhuma regex corresponde. Não altera grupos técnicos, campanhas ou janelas.
