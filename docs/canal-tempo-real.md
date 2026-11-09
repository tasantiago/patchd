# Canal em tempo real e perguntas ao parque

O canal antecipa a entrega de jobs aos agentes conectados. As perguntas coletam respostas de leitura por jobs assinados. São funções relacionadas, com controles e resultados diferentes.

## Habilitar e verificar o canal

Em **Configuração › Parâmetros › Agentes › Canal em tempo real**, mantenha **ligado** para agentes compatíveis. A opção vem ligada por padrão. No Resumo da máquina, confira **Tempo real: conectado**.

O agente abre `POST /api/v1/agent/canal` e espera até 45 segundos. Quando há um job, o servidor avisa e o agente antecipa o check-in que entrega o job. Sem novidade, a espera termina e é aberta novamente. Não há porta de entrada adicional no computador.

O aviso usa notificações do PostgreSQL, e os jobs agendados são conferidos a cada 30 segundos. A espera de cada agente não ocupa uma conexão do pool do banco. Uma falha no canal não elimina o check-in periódico; agentes antigos continuam por ele.

Se usar proxy ou relé, mantenha o prazo de leitura da conexão acima de 45 segundos; o exemplo de HAProxy do [relé](rele.md) usa 90 segundos. Consulte [Intervalo de check-in](operacao.md#intervalo-de-check-in-pela-tela) para ajustar a frequência sem confundir as duas funções.

## Fazer uma pergunta

1. Entre como admin em **Automação › Perguntas ao parque**.
2. Escolha todas as máquinas ou um grupo de implantação.
3. Selecione um sensor e informe o argumento permitido.
4. Envie e acompanhe o agrupamento das respostas. Exporte o CSV para conferir máquina a máquina.

| Sensor | Exemplo de argumento | Resultado e limites |
| --- | --- | --- |
| Arquivo | `C:\Program Files\Empresa\app.exe` ou `/opt/empresa/app` | Existência, tamanho, data e SHA-256 até 100 MB; versão quando disponível no Windows. Não baixa o arquivo. |
| Processo | `firefox.exe` ou `sshd` | Execução, quantidade e sessões quando suportadas. |
| Serviço | `Spooler` ou unidade do systemd | Estado e tipo de início quando disponíveis na plataforma. |
| Registro | Chave em `HKLM\SOFTWARE\...` e nome do valor | Só Windows; não permite SAM/SECURITY. Nomes que indiquem segredo têm o conteúdo ocultado. |
| Programa | Parte do nome do aplicativo | Instalações e versões encontradas. |

O argumento é validado no servidor e no agente. Caminhos de rede, curingas e `..` são recusados. O sensor não executa comandos fornecidos pelo operador. Cada máquina recebe um job `pergunta` com validade de uma hora; com canal conectado, normalmente começa em segundos, sem garantir um prazo para máquinas indisponíveis.

## Interpretar os resultados

- **Não existe / não instalado / não está rodando** é uma resposta do sensor.
- **Sem resposta**, falha de leitura e job expirado precisam ser investigados; não equivalem à ausência do item.
- **Não se aplica neste sistema** indica limite da plataforma.
- **Agente sem as perguntas** exige uma versão compatível do agente.

Uma pergunta representa a observação naquele momento. Para alterar a máquina, use os fluxos de instalação, padronização, scripts ou manutenção apropriados. Só admin cria e consulta perguntas; a criação é auditada como `pergunta.criar`.

Implementação e limites: [sensores](../internal/jobs/query.go), [leitura no agente](../internal/sensor/sensor.go), [canal](../internal/canal/canal.go) e [operação detalhada](operacao.md#canal-em-tempo-real-e-perguntas-ao-parque).
