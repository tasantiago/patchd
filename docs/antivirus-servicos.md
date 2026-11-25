# Evidência do antivírus por serviços

Em Shield › Política › Critérios › Serviços obrigatórios do antivírus, informe
os nomes internos, um por linha, separados por sistema. Não há fabricante
fixo: três serviços Windows e um Linux/macOS são a configuração inicial
esperada; a lista pode mudar sem recompilar o agente.

Windows: ServiceName (Get-Service). Linux: unidade systemd com sufixo .service.
macOS: label do launchd no domínio em que o agente roda (serviço de sistema).
O agente informa os serviços em execução no check-in. A política compara nomes
exatos (exceto caixa no Windows). Todos os nomes exigidos devem aparecer.

Ausência de coleta, coleta com erro, lista não configurada ou estado recebido
há mais de 48 horas: desconhecido. Lista válida e serviço ausente: não
conforme, responsabilidade da TI. Todos presentes: conforme neste critério.
Isso não prova proteção em tempo real, assinaturas atualizadas ou comunicação
com a console do fabricante.

Atualize primeiro servidor e agentes, confira a coleta e só então habilite
o critério. Não há reinício automático de serviços. A política versionada
existente guarda os nomes, sem nova migração SQL.
