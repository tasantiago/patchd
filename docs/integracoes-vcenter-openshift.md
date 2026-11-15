# Integrações: vCenter e OpenShift (desenho)

Versão 0.1, 15/11/2026. Proposta, ainda sem código. Diz **o que** buscar, **por qual API**, **com
que permissão** e **como ligar** ao que o patchd já sabe. As decisões abertas estão no fim.

## 1. O que o patchd ganha

| Pergunta que hoje não tem resposta | De onde vem |
|---|---|
| Quais VMs ligadas **não têm o agente**? (o buraco da cobertura) | vCenter, OpenShift Virtualization |
| Em que host ESXi, cluster e datastore está a máquina? Tem snapshot velho? VMware Tools em dia? | vCenter |
| A versão do ESXi e do vCenter está atrasada? | vCenter (versão e build) + avisos da Broadcom |
| Em que versão está cada cluster OpenShift, e há atualização recomendada? | OpenShift (`ClusterVersion`) |
| Que nós estão fora de `Ready`, com que RHCOS e kernel? Qual VM do vCenter é cada nó? | OpenShift (`Node`) + vCenter |
| As imagens dos containers têm vulnerabilidades graves? | RHACS (se a organização tiver) |

O patchd continua sendo **só leitor** dessas plataformas na primeira versão: nenhuma escrita no
vCenter nem no OpenShift (ver a fase 4).

## 2. Regra geral: conector no servidor, por puxada, como os catálogos

As duas integrações seguem o padrão que os catálogos já usam (`docs/fontes-de-verdade.md`,
"Catálogos do servidor"):

- **O servidor puxa** (o vCenter e o OpenShift não chamam o patchd). Saída da VM do servidor
  para a porta 443 do vCenter e 6443 da API do OpenShift.
- **De tempos em tempos**: um intervalo por conector (sugestão: 15 minutos para o inventário,
  6 horas para versões e avisos), com a primeira passada alguns minutos depois da partida.
- **Uma passada por vez**: a mesma trava do PostgreSQL (`pg_try_advisory_lock`) que impede duas
  sincronizações de catálogo ao mesmo tempo.
- **Registro de cada passada** (`ext_runs`: início, fim, objetos lidos, erro), com o aviso de
  "atrasado" quando a última passada boa passou de 3 intervalos, na aba Saúde.
- **A passada substitui o retrato daquela fonte**: o que sumiu fica marcado como "não visto
  desde", e sai depois de N dias (a VM apagada não some do histórico da máquina na hora).
- **Credenciais só em arquivo** (`PATCHD_VCENTER_PASSWORD_FILE`, `PATCHD_OPENSHIFT_TOKEN_FILE`),
  como `PATCHD_SMTP_PASSWORD_FILE` e `PATCHD_OIDC_CLIENT_SECRET_FILE`: nunca no banco, no painel,
  no log ou na auditoria.
- **TLS conferido sempre**, com a CA da plataforma em arquivo (`PATCHD_VCENTER_CA_FILE`,
  `PATCHD_OPENSHIFT_CA_FILE`). Nada de "ignorar certificado".
- **Vários vCenters e vários clusters**: cada um é uma fonte, com nome, endereço, credencial e
  CA próprios. Os endereços reais ficam no `.env`, nunca no repositório.

### Banco (proposta)

```
ext_sources  (id, kind 'vcenter'|'openshift', name, url, enabled, interval)
ext_runs     (source_id, started_at, finished_at, objects, error)
ext_objects  (source_id, kind, ext_id, name,
              uuid_key, serial, macs text[], ips inet[],     -- para ligar à máquina
              power, os, attrs jsonb,                        -- o resto, por tipo
              first_seen, last_seen, gone_at,
              machine_id NULL, link_by NULL)                 -- a ligação e por qual regra
```

`kind` em `ext_objects`: `vm`, `esxi`, `vcenter`, `cluster-ocp`, `node`, `vmi` (VM do OpenShift
Virtualization). `attrs` guarda o que só aquele tipo tem (snapshot, Tools, versão do RHCOS...),
sem uma coluna nova a cada campo. Gravação em lote (`COPY`), e só das linhas que mudaram.

## 3. Ligar a VM à máquina do agente

O agente já manda o UUID e o número de série do SMBIOS, e o servidor já os compara nas duas
ordens de bytes (`identity.UUIDKey`): o SMBIOS 2.6 grava os três primeiros campos em
little-endian, e leitores diferentes mostram a mesma VM com bytes em ordens diferentes.

| Ordem | Regra | Confiança |
|---|---|---|
| 1 | `UUIDKey(UUID da BIOS da VM)` = `UUIDKey(UUID do SMBIOS do agente)` | alta |
| 2 | um MAC da VM igual a um MAC do inventário da máquina | alta (confirma a 1, desempata clones) |
| 3 | número de série: no VMware, `VMware-42 1c ...` sai dos mesmos bytes do UUID | alta |
| 4 | nome (`guest.hostName` × hostname do agente) | **fraca**: só sugere, não liga sozinho |

- vCenter: `config.uuid` é o UUID da BIOS (o que o convidado vê); `config.instanceUuid` é o
  identificador único da VM **no vCenter**, e é o `ext_id` (a VM clonada com o mesmo UUID da
  BIOS tem outro `instanceUuid`; por isso a regra 2 desempata).
- OpenShift Virtualization: o UUID do SMBIOS da VM está em `spec.template.spec.domain.firmware.uuid`
  (conferir o caminho na versão instalada; desde a 4.20 ele é preenchido nas VMs existentes).
- Nó do OpenShift: `status.nodeInfo.systemUUID` é o UUID do SMBIOS do nó. Se os nós forem VMs
  do vCenter, a regra 1 liga **nó ↔ VM**, e o painel mostra em qual host ESXi cada nó roda.

O que não liga vira a lista **"VMs sem agente"** (ligadas, com sistema que o patchd atende),
ao lado da Descoberta.

## 4. vCenter

### Qual API

| Opção | A favor | Contra |
|---|---|---|
| **vSphere Web Services pelo `govmomi`** (recomendada) | o modelo completo (VM, host, cluster, snapshot, Tools); **PropertyCollector + ContainerView** lê milhares de VMs numa ida, só com as propriedades pedidas; vem com o **`vcsim`**, um vCenter simulado para os testes (como o PostgreSQL de teste) | dependência grande; SOAP por baixo |
| VI/JSON (o mesmo modelo, em HTTP/JSON) | só a biblioteca padrão do Go; mesmo PropertyCollector e ContainerView | só a partir do **vCenter 8.0 Update 1**; teríamos de escrever os tipos |
| vSphere Automation API (`/api/vcenter/vm`) | REST simples, sessão por `POST /api/session` | **lista até 4.000 VMs e falha acima disso** (é preciso fatiar por datacenter ou pasta); detalhes de cada VM em chamadas separadas (uma por VM): lento com milhares |

Recomendação: **`govmomi`**, pelo `vcsim` nos testes e por funcionar em qualquer vCenter
suportado. Se todos os vCenters forem 8.0 U1 ou mais novos e a dependência pesar, a VI/JSON faz
a mesma leitura sem biblioteca externa. A Automation API só para o que só ela tem (o
vSphere Lifecycle Manager, na fase 3).

### O que ler, numa passada

Um `ContainerView` sobre `VirtualMachine` e outro sobre `HostSystem`, a partir da raiz, e um
`RetrievePropertiesEx` (com a continuação, `ContinueRetrievePropertiesEx`, que o `govmomi` já
segue) pedindo só estas propriedades:

- **VM**: `name`, `config.uuid`, `config.instanceUuid`, `config.template` (fica de fora),
  `config.version` (versão do hardware virtual), `config.guestFullName`, `guest.guestFullName`,
  `guest.hostName`, `guest.ipAddress`, `guest.net` (MACs e IPs), `guest.toolsRunningStatus`,
  `guest.toolsVersionStatus2`, `runtime.powerState`, `runtime.host`, `snapshot` (quantos e a
  data do mais antigo), `summary.config.numCpu`, `summary.config.memorySizeMB`, `resourcePool`,
  `parent` (pasta).
- **Host ESXi**: `name`, `summary.config.product.version` e `.build`, `runtime.connectionState`,
  `runtime.inMaintenanceMode`, `hardware.systemInfo` (fabricante, modelo, UUID, série), `parent`
  (cluster).
- **vCenter**: `ServiceContent.about` (versão e build).

**Não ler** `config.annotation` nem os atributos personalizados (`customValue`): é texto livre
onde as equipes às vezes deixam senha ou contato. Se um dia for preciso, entra com a mesma
regra das perguntas ao parque (nomes que parecem de senha voltam ocultos).

Ordem de grandeza: com 7.000 VMs e essas ~20 propriedades, a passada é de poucos MB e poucos
segundos no vCenter. A versão 2 pode trocar a releitura a cada 15 minutos por
`WaitForUpdatesEx` (o vCenter avisa só o que mudou), com uma releitura completa por dia.

### Permissão

- Uma conta só do patchd (local `vsphere.local` ou do AD), com o papel **Read-only** do vCenter
  na raiz, **propagando** para baixo. É o suficiente para tudo da fase 1.
- Fase 3 (conformidade do vLCM): mais o privilégio de leitura do Lifecycle Manager
  (`VcIntegrity.lifecycleSoftwareSpecification.Read`), num papel próprio.
- A sessão abre no começo da passada e fecha no fim (o vCenter limita sessões simultâneas).
- A CA: o certificado raiz do VMCA (baixado do próprio vCenter) ou a CA da organização que
  assinou o certificado dele.

### Versões e avisos de segurança (fase 3)

- A versão e o build de cada ESXi e do vCenter vêm na passada. O que falta é a régua: "qual o
  build corrigido para este aviso". A Broadcom publica os VMSA em JSON (o feed antigo da VMware e
  uma API no portal de suporte), mas há relatos de 403 em chamadas automáticas: **conferir antes
  de depender**, e manter a régua manual (o build mínimo por versão, em Configuração) como
  reserva.
- Clusters gerenciados por imagem no vSphere Lifecycle Manager: a API de conformidade do cluster
  (`/api/esx/settings/clusters/{cluster}/software/compliance`) diz se cada host está na imagem.
  É a resposta do próprio vSphere, melhor que comparar builds.

## 5. OpenShift

### Qual API

A API do Kubernetes por HTTP com token, **sem o `client-go`**: o patchd só precisa de `GET` em
poucos recursos, com paginação (`limit` e `continue`). A biblioteca padrão e structs pequenos
bastam, como no resto do projeto.

### O que ler

| Recurso | Para quê |
|---|---|
| `clusterversions.config.openshift.io` (`version`) | versão atual, canal, `availableUpdates` (as recomendadas), `conditionalUpdates` (as que dependem de condições, com o risco), e se há atualização em andamento ou falhando |
| `clusteroperators.config.openshift.io` | operadores `Degraded` ou indisponíveis |
| `nodes` | nome, papéis, `Ready`, `nodeInfo` (`osImage` = versão do RHCOS, kernel, kubelet, `systemUUID`) |
| `machines.machine.openshift.io` (opcional) | qual máquina da plataforma é cada nó |
| `virtualmachines`/`virtualmachineinstances.kubevirt.io` (se houver OpenShift Virtualization) | as VMs, o UUID do firmware, o sistema do convidado (`guestOSInfo`, precisa do qemu-guest-agent), ligadas ou não |

### Permissão

- Uma `ServiceAccount` própria (`patchd-leitor`, num namespace próprio), com um `ClusterRole`
  **feito para ela**: só `get`/`list` nos recursos da tabela acima. Melhor que o `cluster-reader`
  pronto, que lê muito mais do que o patchd precisa.
- O token: um Secret de token da ServiceAccount (longa duração), guardado no arquivo do
  `PATCHD_OPENSHIFT_TOKEN_FILE`, com troca agendada (ex.: anual) e o procedimento em
  `docs/operacao.md`. Conferir com `oc auth can-i list nodes --as=system:serviceaccount:...`.
- A CA da API (a do `kubeconfig` do cluster).

### O que o patchd **não** faz no OpenShift

- **Não instala o agente nos nós (RHCOS).** O sistema dos nós é imutável e atualizado pelo
  próprio cluster (a atualização do OpenShift traz o RHCOS junto, pelo Machine Config Operator).
  Instalar pacote ou agente no nó foge do suporte. O nó aparece no patchd como "gerenciado pelo
  OpenShift", com a conformidade medida pela versão do cluster.
- **As VMs do OpenShift Virtualization recebem o agente normalmente**: são Windows/RHEL comuns. O
  conector só as acha e as liga à máquina (regra 1), e as sem agente entram em "VMs sem agente".

### Vulnerabilidade dos containers (fase 3)

Não refazer no patchd o que um scanner de imagens faz. Se a organização tiver o **Red Hat
Advanced Cluster Security (RHACS)**, o patchd lê o resumo pela API dele
(`/v1/export/vuln-mgmt/workloads`, que devolve cada deployment com as imagens e as CVEs, em
fluxo) com um token de papel só de leitura, e mostra por cluster: imagens com CVE crítica, as
exploradas (cruzando com o KEV que o patchd já baixa). O Container Security Operator (Quay +
Clair) está **descontinuado** pela Red Hat em favor do RHACS: não vale construir em cima dele.

## 6. No painel

- **Configuração › Integrações**: as fontes (nome, tipo, endereço, intervalo, ligada), a última
  passada, o erro, e o botão "testar agora" (lê só a versão: confere TLS, credencial e
  permissão). A credencial não aparece nem é editada pela tela: fica no arquivo.
- **Página da máquina**, bloco "Virtualização": vCenter, cluster, host ESXi, datastore, Tools,
  hardware virtual, snapshots (com alerta acima de N dias), ou cluster OpenShift e namespace.
- **Parque**: filtro "VM" / "física" / "nó OpenShift"; a lista **"VMs sem agente"**.
- **Infraestrutura** (tela nova): os clusters OpenShift (versão, atualização recomendada,
  operadores degradados, nós fora de `Ready`) e os ESXi (versão, build, manutenção).
- **Alertas** (o monitoramento existente): snapshot velho, Tools parado, nó fora de `Ready`,
  operador degradado, cluster com atualização falhando.

## 7. Fases

1. **vCenter, só inventário**: conector, tabelas, ligação VM ↔ máquina, "VMs sem agente", bloco
   na página da máquina. Testes com o `vcsim`.
2. **OpenShift**: clusters, versão e atualizações, nós, ligação nó ↔ VM, VMs do OpenShift
   Virtualization.
3. **Régua de segurança**: ESXi/vCenter contra os avisos (ou o vLCM), resumo do RHACS, alertas.
4. **Escrita (opcional, separada)**: por exemplo, snapshot da VM antes da campanha de patch e a
  remoção depois. Pede um papel com privilégio de snapshot, outra credencial, a opção ligada
  campanha a campanha, e auditoria de cada chamada. Só se a fase 1 provar valor.

## 8. Segurança

- Contas só de leitura, uma por plataforma, sem uso por pessoas; troca da senha/token agendada.
- Credencial só em arquivo com permissão restrita; nunca no banco, no log, na auditoria ou na
  tela. O teste de conexão não mostra a resposta crua (pode trazer nomes internos).
- TLS sempre conferido, com a CA própria de cada fonte.
- Nada do patchd vai para o vCenter ou o OpenShift (só os pedidos de leitura).
- O texto livre das plataformas (anotações, atributos, labels) não entra por padrão.
- O endereço e o nome real de cada vCenter e cluster só no `.env` e no banco, nunca no
  repositório nem nos exemplos.
- Uma falha de rede ou de credencial não apaga o retrato anterior: só marca a fonte como
  atrasada.

## 9. Decisões abertas

1. Versão dos vCenters (se todos forem 8.0 U1 ou mais novos, a VI/JSON vira opção sem dependência).
2. Quantos vCenters e quantos clusters OpenShift, e se a VM do servidor do patchd alcança todos.
3. Há OpenShift Virtualization? Há RHACS?
4. Os clusters são desconectados (sem acesso ao serviço de atualização da Red Hat)? Se forem, o
  `availableUpdates` vem do OpenShift Update Service interno, ou vazio.
5. Os nós do OpenShift são VMs do vCenter, ou bare metal?

## Referências

- vSphere Automation API, lista de VMs (limite de 4.000): https://developer.broadcom.com/xapis/vsphere-automation-api/latest e https://knowledge.broadcom.com/external/article/301570/obtaining-information-for-more-than-4000.html
- PropertyCollector e ContainerView pela VI/JSON (vCenter 8.0 U1 em diante): https://blogs.vmware.com/cloud-foundation/2024/10/21/efficient-data-retrieval-with-vi-json-api-propertycollector-and-containerview/
- `govmomi` (`property`, `view`, `simulator`): https://pkg.go.dev/github.com/vmware/govmomi/property
- Conformidade de cluster no vLCM: https://developer.broadcom.com/xapis/vsphere-automation-api/8.0.3/esx/settings/clusters-software-compliance/
- Feed JSON dos VMSA: https://knowledge.broadcom.com/external/article/322729/vmware-security-advisories-json-feed.html e https://knowledge.broadcom.com/external/article/408302/json-api-for-product-security-advisories.html
- `ClusterVersion` (`availableUpdates`, `conditionalUpdates`): https://docs.redhat.com/en/documentation/openshift_container_platform/4.19/html/config_apis/clusterversion-config-openshift-io-v1
- RBAC do OpenShift: https://docs.redhat.com/en/documentation/openshift_container_platform/latest/html/authentication_and_authorization/using-rbac
- Informações do convidado no KubeVirt: https://kubevirt.io/user-guide/user_workloads/guest_agent_information/ e UUID do firmware: https://kubevirt.io/user-guide/compute/virtual_hardware/
- RHACS, exportação de vulnerabilidades: https://docs.redhat.com/en/documentation/red_hat_advanced_cluster_security_for_kubernetes/4.6/html/api_reference/vuln-mgmt-service
- Container Security Operator descontinuado: https://docs.redhat.com/en/documentation/red_hat_quay/3/html/red_hat_quay_operator_features/container-security-operator-setup
