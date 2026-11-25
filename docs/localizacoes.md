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
e auditada. O cadastro é uma base reutilizável; filtros de inventário e campanhas
por andar não fazem parte desta implementação.
