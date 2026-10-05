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
- **RF-08** Executar apenas jobs tipados (instalar atualização, instalar pacote, agendar reboot, re-scan) e verificar o resultado por novo scan, nunca pelo código de saída. **decidido (padrão)**
- **RF-09** Autodiagnóstico: desvio de relógio, conectividade, permissões, fonte indisponível.
- **RF-10** Autoatualização do agente, apenas com binário assinado.
- **RF-11** Registrar o antivírus ativo como fato, sem avaliar assinaturas. **padrão**

## Funcionais: servidor

- **RF-20** API REST para enrollment, check-in, inventário e resultados de jobs.
- **RF-21** Catálogo de vulnerabilidades: MSRC CVRF, USN/OSV (Ubuntu), advisories do Fedora, releases de segurança da Apple, CISA KEV. Relação advisory ↔ pacote muitos-para-muitos.
- **RF-22** Nível alvo por SO: UBR mínimo; versão de pacote corrigida segundo o advisory da distro (nunca a versão do upstream); versão mínima do macOS por major.
- **RF-23** Estados de compliance: em dia, faltando, reboot pendente, sem dado recente, desconhecido.
- **RF-24** Identidade sem duplicatas: casamento por evidências, alerta em colisão (nunca fusão silenciosa), formatação = nova instalação ligada ao mesmo ativo.
- **RF-25** Fila de jobs com máquina de estados, anéis (piloto primeiro), janelas e aviso de reboot.
- **RF-26** Painel web e exportações CSV e Google Sheets.
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

## Não funcionais

- **RNF-01** Plataformas: Windows 10/11 amd64 (arm64 apenas compila); Ubuntu 26.04 e Fedora 44, amd64 e arm64; macOS 13 ou superior, arm64 e amd64 (piso imposto pelo Go 1.27).
- **RNF-02** Binário único por SO e arquitetura, Go 1.27, `CGO_ENABLED=0`.
- **RNF-03** Pegada leve: check-in a cada 1 hora com jitter; scan completo 1 vez ao dia. **padrão**
- **RNF-04** Funciona offline: fila local de resultados; scan offline no Windows.
- **RNF-05** Segurança: TLS; jobs e binários assinados com ed25519; servidor em container não-root; segredos fora da imagem e do repositório.
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
- **R-04** A rede faz inspeção TLS com CA corporativa; os containers precisam confiar nela. Saída do servidor para a internet (direta ou por proxy): **pendente**.
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
