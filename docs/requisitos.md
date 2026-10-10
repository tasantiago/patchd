# Requisitos do patchd

Versão 0.1, consolidada ao fim do Módulo 0 (Aulas 0.1 a 0.4). Toda mudança de requisito entra por commit neste arquivo.

Legenda de status:
- **decidido**: escolha feita explicitamente.
- **padrão**: adotado pelo valor sugerido na Aula 0.4; pode ser revisto a qualquer momento.
- **pendente**: depende de um fato do ambiente ainda não levantado.
- **futuro**: aceito para depois da v1; não entra no escopo do go-live.

Regra deste repositório: nenhum nome de host, IP, domínio interno, serial ou certificado do ambiente real é escrito aqui. Esses valores ficam em variáveis de ambiente e no `.env` local (ignorado pelo git).

## Princípios

- **P-01** O agente coleta e executa; o servidor decide.
- **P-02** Compliance = resultado bom + dado fresco. Instalado não é efetivo: o reboot separa um do outro.
- **P-03** Todo dado coletado registra a fonte usada (ver `fontes-de-verdade.md`).
- **P-04** Nenhum parser lê texto localizado (títulos, datas, mensagens). Comandos rodam com `LC_ALL=C`; no Windows, registro, WMI e COM.

## Funcionais: agente

- **RF-01** Coletar evidências de identidade (instalação: MachineGuid, `/etc/machine-id`; hardware: UUID de SMBIOS, serial, `platform_UUID`, MACs) e guardar o ID emitido pelo servidor no enrollment.
- **RF-02** Coletar versão do SO (build + UBR; `os-release`; versão + build do macOS), arquitetura, último boot e relógio local.
- **RF-03** Coletar software instalado: chaves Uninstall (64 bits, WOW6432Node, HKU); `dpkg-query`; `rpm`; `system_profiler` + `pkgutil`.
- **RF-04** Escanear atualizações faltantes: WUA online (via WSUS) e offline (`wsusscn2.cab`); `apt-get -s` + `pro security-status`; `dnf advisory --json`; `softwareupdate --list`.
- **RF-05** Detectar reboot pendente com pelo menos duas fontes por SO, quando existirem.
- **RF-06** Coletar o estado do atualizador nativo (política de Windows Update/WSUS, `apt-config`, dnf automatic, preferências do `softwareupdate`).
- **RF-07** Informar o frescor: data dos metadados de pacotes e do último scan bem-sucedido.
- **RF-08** Executar apenas jobs tipados (instalar atualização, instalar pacote, agendar reboot, re-scan) e verificar o resultado por novo scan, nunca pelo código de saída. **decidido (padrão)** Primeiro tipo (Aula 8.1): `rescan`. O tipo é uma lista fechada no agente; parâmetro, campo ou tipo desconhecido = job recusado. O servidor julga pelo efeito: o `rescan` só fica concluído quando chega uma busca recebida depois da entrega. Segundo tipo (Aula 8.4): `update`, só no Linux, com a lista fechada de pacotes binários e a versão mínima de cada um (escolhidos pelas pendências no painel, ou `-packages` na linha de comando). O agente atualiza as listas de pacotes (`apt-get update`, `dnf makecache --refresh`), confere numa busca feita na hora que cada pacote está pendente com candidata pelo menos na versão pedida (senão, rejeitado), instala pelo apt ou pelo dnf numa unidade transitória do systemd (fora do ProtectSystem do serviço) e faz a busca; o servidor conclui só se a busca nova não mostrar nenhum pacote pedido abaixo da versão pedida. O update não reinicia a máquina (reinício: job próprio, **pendente**).
- **RF-09** Autodiagnóstico: desvio de relógio, conectividade, permissões, fonte indisponível.
- **RF-10** Autoatualização do agente, apenas com binário assinado.
- **RF-11** Registrar o antivírus ativo como fato, sem avaliar assinaturas. **padrão**

## Funcionais: servidor

- **RF-20** API REST para enrollment, check-in, inventário e resultados de jobs.
- **RF-21** Catálogo de vulnerabilidades: MSRC CVRF, USN/OSV (Ubuntu), advisories do Fedora, releases de segurança da Apple, CISA KEV. Relação advisory ↔ pacote muitos-para-muitos.
- **RF-22** Nível alvo por SO: UBR mínimo; versão de pacote corrigida segundo o advisory da distro (nunca a versão do upstream); versão mínima do macOS por major.
- **RF-23** Estados de compliance: em dia, faltando, reboot pendente, sem dado recente, desconhecido. Regra (Aula 7.4), em ordem: sem contato há mais de 7 dias (RNF-12) = sem dado recente; sem inventário ou sem catálogo para o SO = desconhecido; correção pendente (ou major do macOS sem suporte) = faltando; reinício pendente (pela busca ou por kernel mais novo instalado) = reboot pendente; senão, em dia. Os programas de terceiros ficam fora do estado por enquanto.
- **RF-24** Identidade sem duplicatas: casamento por evidências, alerta em colisão (nunca fusão silenciosa), formatação = nova instalação ligada ao mesmo ativo. A decisão do administrador diante do alerta é aposentar o registro antigo (`patchd-server machine retire`, Aula 7.5): ele sai da frota e a credencial dele deixa de valer, sem apagar o histórico; `restore` desfaz.
- **RF-25** Fila de jobs com máquina de estados, anéis (piloto primeiro), janelas e aviso de reboot. Fila e estados (Aula 8.1): pendente → entregue → concluído, falhou ou rejeitado; pendente → cancelado ou expirado (prazo de 10 min a 7 dias, 24 h por padrão). Entrega no check-in, no máximo uma vez, só a agentes que anunciam a capacidade `jobs-v1`; o resultado volta por `POST /api/v1/agent/jobs/{id}/result` e fica guardado no agente enquanto o servidor estiver fora. Pelo painel (Aula 8.2): o admin pede "Buscar atualizações agora" no detalhe da máquina (job `rescan`, prazo de 3 h, autor = usuário do painel; um segundo pedido com o primeiro em aberto não cria outro) e cancela jobs pendentes; a tela Jobs lista os mais recentes da frota, para os dois perfis. Job para máquina aposentada não é criado (conferido na própria gravação). Anéis e janelas (Aula 8.3): cada máquina num anel de 0 (piloto) a 9 (padrão 1, `ring set`); cada anel com uma janela semanal no horário local de um fuso (`ring window -days seg-sex -start 12:00 -duration 2h`). `job create -window` cria o job para a próxima janela do anel (`-ring N`: um para cada máquina do anel); o início vai assinado no payload (`not_before`), o servidor só entrega a partir dele e o agente confere de novo. O pedido pelo painel continua imediato (o `rescan` não muda a máquina). Campanhas (Aula 8.5, liberação automática decidida na 8.4): o mesmo pedido (`rescan`, ou `update` de certas fontes) passa pelos anéis em ordem; o servidor libera o primeiro anel e, a cada minuto, confere o anel atual: todos os jobs com sucesso e o tempo de observação passado, cria os jobs do anel seguinte (no update, só para as máquinas que têm alguma das fontes pendente); qualquer job sem sucesso pausa a campanha até o admin retomar (aceitando as falhas) ou cancelar. Criação e controle pela linha de comando (`campaign`), acompanhamento também no painel. Só um servidor avança as campanhas por vez (trava consultiva do PostgreSQL, Aula 8.5b): outro processo no mesmo banco pula a vez, sem liberar o mesmo anel duas vezes. Job de reinício com aviso: **pendente** (8.6).
- **RF-26** Painel web e exportações CSV e Google Sheets. Telas (Aula 7.6): a frota com o estado de cada máquina e o filtro por estado, o detalhe (pendências, alertas de identidade) e as aposentadas; aposentar e restaurar são ações do perfil admin, com o usuário registrado. Exportação (Aula 7.8): CSV da frota (com o filtro da tela) e das pendências de cada máquina, no painel e em `compliance -all -csv`; separador ";" e UTF-8 com BOM (Excel em português), campos que começam com = + - @ neutralizados (injeção de CSV). O Google Sheets importa o mesmo arquivo; gravar direto numa planilha pela API do Google fica **pendente**.
- **RF-27** Distinguir "faltando" de "publicado pela Microsoft, mas não aprovado no WSUS".
- **RF-28** Ponto de distribuição: o servidor guarda e serve `wsusscn2.cab`, pacotes de terceiros e binários do agente, com manifesto assinado (ed25519 + SHA-256). O agente baixa apenas do servidor.
- **RF-29** Cache sob demanda dos repositórios Ubuntu e Fedora, preservando a verificação GPG das distros. **padrão**
- **RF-30** Na v1, as atualizações do SO Windows são instaladas pelo WSUS; o patchd mede e cobra. **padrão**
- **RF-31** Terceiros no Windows: catálogo baseado nos manifestos do winget-pkgs (apenas os dados), Chocolatey como conferência opcional; instalador baixado e verificado pelo servidor e executado pelo próprio agente. Nenhum Chocolatey ou winget nas estações. **padrão**
- **RF-32** O inventário de software vem das chaves Uninstall, nunca de um gerenciador de pacotes de terceiros.
- **RF-33** O servidor pode distribuir LCU e .NET do próprio cache, com instalação pelo agente via DISM, ativada por anel, como substituto gradual do WSUS.
- **RF-34** Lista de aplicativos gerenciados definida pela equipe; nada é instalado só por existir no catálogo.
- **RF-35** Self-service: portal em que o usuário escolhe, entre os aplicativos homologados para o SO da própria máquina, o que quer instalar; o pedido vira job e o agente instala como SYSTEM, sem o usuário ser administrador. Com aprovação opcional por aplicativo, limite de pedidos e auditoria. **futuro (depois do go-live)**
- **RF-36** Usuário com sessão ativa no inventário, para o vínculo usuário × máquina do self-service. **futuro**

- **RF-37** Duas instâncias ativas: uma na rede interna e uma na nuvem, para quem está fora da rede (home office, viagem). O agente conhece os dois endereços e usa o disponível; os dados convergem para a instância interna, dona das decisões (P-01). A credencial vale para a implantação do patchd, não para um endereço. **futuro**
- **RF-38** Login do painel com dois perfis, admin e leitura. Na v1: usuários do AD por LDAPS, com o perfil vindo de grupos (Aula 7.3), e usuários locais de contingência, com senha em PBKDF2 (Aula 7.2). Em produção: RHBK (Red Hat build of Keycloak) por OIDC. Sessão no servidor, com cookie `HttpOnly` e `SameSite=Strict`, prazo de 8 h e 30 min de inatividade; as rotas de leitura da API exigem a sessão. **decidido**

## Não funcionais

- **RNF-01** Plataformas: Windows 10/11 amd64 (arm64 apenas compila); Ubuntu 26.04 e Fedora 44, amd64 e arm64; macOS 13 ou superior, arm64 e amd64 (piso imposto pelo Go 1.27).
- **RNF-02** Binário único por SO e arquitetura, Go 1.27, `CGO_ENABLED=0`.
- **RNF-03** Pegada leve: check-in a cada 1 hora com jitter; scan completo 1 vez ao dia. **padrão**
- **RNF-04** Funciona offline: fila local de resultados; scan offline no Windows.
- **RNF-05** Segurança: TLS; jobs e binários assinados com ed25519; servidor em container não-root; segredos fora da imagem e do repositório. Jobs (Aula 8.1): assinados na criação pela chave de jobs do servidor (ed25519, separada da chave de release, que nunca vai ao servidor) e guardados assinados, de modo que uma alteração direta no banco é recusada pelo agente; a chave pública vai embutida no agente no build (`PATCHD_JOB_PUBKEY`). Com `PATCHD_JOB_SIGNING_KEY_FILE` no servidor (Aula 8.2), a chave privada fica na memória do processo, para os pedidos do painel: quem tomar o servidor pode assinar jobs, limitados aos tipos fechados. Servidor, agente e `job pubkey` mostram a mesma impressão curta (`key_id`) quando usam o mesmo par. O agente confere assinatura, máquina de destino, validade (com 5 min de tolerância de relógio) e número sempre crescente (não aceita repetição).
- **RNF-06** Independente de idioma (ver P-04).
- **RNF-07** Escala dimensionada pelo tamanho da frota. **pendente**
- **RNF-08** Logs em JSON com `slog`, métricas e backup do PostgreSQL.
- **RNF-09** Configuração só por variáveis de ambiente e flags; healthcheck e desligamento limpo.
- **RNF-10** Português do Brasil em logs, mensagens e painel.
- **RNF-11** Cache com limite de espaço e limpeza por idade. **pendente**
- **RNF-12** "Sem dado recente" após 7 dias sem check-in válido. **padrão**
- **RNF-13** Instância exposta à internet: TLS obrigatório, registro de máquinas só pela rede interna, limite de requisições por IP, avaliação de certificado de cliente (mTLS) e conformidade com a política do tribunal para dados em nuvem pública. **futuro**

## Restrições do ambiente

- **R-01** Frota Windows gerenciada por WSUS interno via GPO, com instalação automática agendada diariamente.
- **R-02** Estações Fedora ingressadas no Active Directory (SSSD).
- **R-03** macOS sem MDM, com os limites do `softwareupdate` (Aula 8.4).
- **R-04** A rede faz inspeção TLS com CA corporativa; os containers precisam confiar nela. Saída do servidor para a internet (direta ou por proxy): **pendente** para produção. No laboratório (Aula 6.6): saída direta, sem variáveis de proxy, e os certificados das fontes do catálogo chegam com os emissores públicos (sem inspeção nesse caminho). Se a produção tiver proxy, o servidor usa `HTTPS_PROXY`; com inspeção, o `gdmf.apple.com` precisa de exceção no proxy, porque a raiz da Apple é fixada no binário. O firewall da rede é transparente, sem autenticação de usuário (Aula 6.6): as máquinas também saem direto, e o cache de repositórios é economia de banda e controle, não o único caminho (sem ele, o agente devolve a máquina à origem).
- **R-05** Nada roda em produção antes do Módulo 8, e lá só no anel piloto.
- **R-06** Sem cache de atualizações do macOS pelo patchd; só via Content Caching da Apple, fora do projeto.

## Fora de escopo (v1)

- Assinaturas e políticas de antivírus.
- ITAM financeiro: licenças, contratos, custos.
- MDM e perfis de configuração do macOS.
- Upgrades de versão principal (feature update do Windows, major do macOS); o patchd apenas mostra que existem. **padrão**
- Monitoramento de desempenho.
- Espelho completo de repositórios Linux.
- Replicar o WSUS na v1.

## Fatos pendentes

- Tamanho da frota (Windows, Fedora, macOS).
- `dnf-automatic` ativo nas estações Fedora.
- Saída do servidor para a internet: direta ou por proxy.
- Espaço em disco disponível para o cache.
- Licenças Windows Enterprise/Education e assinatura Azure (viabilidade do Microsoft Connected Cache).

## Rastreabilidade

Cada aula cita os requisitos que atende. No go-live (Aula 9.4), esta lista é conferida item a item.
