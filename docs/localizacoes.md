# Localizações físicas — prédios e andares

Em **Configuração › Localizações**, o administrador vê os prédios com máquinas
em mais de uma faixa das regras de IP dos grupos de implantação. O prédio
continua vindo do patrimônio e a faixa mais específica prevalece. O cadastro
independe de ativar o Shield ou de atingir o mínimo de máquinas no ranking.

Preencha o nome do andar ao lado de cada faixa (Térreo, 1º andar, etc.) e grave
por prédio. Use exatamente o mesmo nome para duas VLANs do mesmo andar. Os
nomes são específicos do prédio e da faixa, e os vínculos cadastrados continuam
visíveis mesmo se não houver máquinas na faixa naquele momento.

O Shield usa os nomes na próxima avaliação e agrega as faixas com o mesmo nome
dentro do prédio. A regra de privacidade do ranking permanece. As faixas não
são mostradas na Central. Os nomes legados da política Shield continuam valendo
quando não houver um vínculo novo; o campo vazio remove apenas o novo vínculo.
O cadastro não modifica os grupos de implantação nem inicia jobs.

A migração 0084 cria a tabela de vínculos físicos. A alteração é administrativa
e auditada. Os andares cadastrados aqui são os mesmos que as campanhas por prédio e
andar, a página Automação › Prédios e implantação e o Shield usam, pelo mesmo resolvedor
de localização (docs/implantacao-por-predio.md).

## Como os nomes aparecem

Os nomes de prédio e sala vêm do patrimônio, quase sempre em maiúsculas, e as telas os
mostram formatados ("FÓRUM CENTRAL" vira "Fórum Central", "BLOCO A" fica "Bloco A"). A chave
continua sendo o texto do patrimônio, sem mudança: filtros, regras e CSV usam o original.

As siglas que devem ficar em maiúsculas além das genéricas (algarismos romanos e TI) — as da
instituição — ficam em **Configuração › Parâmetros › Aparência › Siglas nos nomes de lugar**
(separadas por vírgula; letras e números, até 12 cada). Nenhuma sigla de instituição é
gravada no código.

## Regras independentes por andar

Todos os prédios do patrimônio são apresentados, mesmo sem equipamentos ou faixas prévias. Em cada prédio, cadastre nome do andar e regex Go/RE2 para o IPv4 principal (ex.: `^10\.12\.1\.[0-9]+$`). Regras iguais em prédios diferentes são independentes. Regras com o mesmo nome agregam; nomes diferentes que correspondem ao mesmo IP deixam o andar sem atribuição. A tela mostra contagens por regra, sem correspondência e conflitos. As contagens por regra podem se sobrepor. Faixas antigas continuam como fallback quando nenhuma regex corresponde. Alterar uma regra não muda campanhas já criadas (os alvos e o andar de cada um ficam gravados na criação); nas próximas liberações, a máquina que saiu do andar escolhido não recebe job. As janelas de manutenção são por prédio, em Automação › Prédios e implantação.
