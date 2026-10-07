# Fontes de verdade por SO

Versão 0.2, revisada ao fim do Módulo 2 com o que foi verificado na prática nos coletores do agente
(Windows 11 25H2, Ubuntu 26.04, Fedora 44 e macOS 26).

Uma **fonte de verdade** é o lugar onde o próprio SO guarda a informação que ele usa para decidir. Tudo o que apenas formata essa informação é uma **visão**, e visões não entram no inventário.

Critérios de escolha, em ordem:
1. Autoritativa: o SO decide com base nela.
2. Estruturada: registro, COM/WMI, JSON, plist ou formato definido pelo agente antes de texto livre.
3. Independente de idioma.
4. Legível sem rede.
5. Estável entre versões do SO.

Quando a melhor fonte falha, o agente usa a próxima e registra qual usou (P-03). Toda fonte é **validada contra itens conhecidos e contra uma segunda fonte**, não só contra a contagem que ela mesma produz.

## Quadro principal

| Dado | Windows | Ubuntu | Fedora | macOS |
|---|---|---|---|---|
| Versão do SO | Registro `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`: `CurrentBuild`, `UBR`, `DisplayVersion`, `EditionID`. Windows 11 = `CurrentBuild` ≥ 22000 | `/etc/os-release` (alternativa `/usr/lib/os-release`) + `/proc/sys/kernel/osrelease` | idem | `/System/Library/CoreServices/SystemVersion.plist` via `plutil -convert json` + `uname -r` |
| Nível de patch | `build.UBR`, comparação numérica com pontos | versão de cada pacote, regra do dpkg | EVR de cada pacote, regra do rpm | versão + build do SO, mais apps do cryptex (Safari) |
| Software instalado | Chaves Uninstall: visão de 64 bits, visão de 32 bits (flag `WOW64_32KEY`, fisicamente em `WOW6432Node`) e `HKU\<SID>` dos perfis carregados | `dpkg-query -W --showformat` com TAB: estado, pacote, versão, arquitetura, pacote fonte e versão do fonte | `rpm -qa --queryformat` com TAB: nome, época, versão, release, arquitetura, `.src.rpm`, data de instalação, fornecedor | `system_profiler SPApplicationsDataType -json` + pasta do cryptex de apps + recibos do `pkgutil --pkg-info-plist` (XML) |
| O que falta | WUA `IUpdateSearcher` online (reflete aprovações do WSUS) e offline com `wsusscn2.cab` (reflete publicações da Microsoft) | `LC_ALL=C apt-get -s dist-upgrade` (linhas `Inst`) + `pro security-status --format json` | `dnf advisory list --security --json` | `softwareupdate --list` (texto) |
| Histórico (auditoria, não estado) | WUA `QueryHistory` | `/var/log/dpkg.log` (formato fixo) | a definir | `system_profiler SPInstallHistoryDataType -json` |
| Reboot pendente | `Microsoft.Update.SystemInfo` `RebootRequired`; chaves `Component Based Servicing\RebootPending` e `WindowsUpdate\Auto Update\RebootRequired` | `/var/run/reboot-required` + `.pkgs`; kernel em execução × mais novo instalado | kernel em execução × mais novo instalado | a definir (Aula 3.6) |
| Atualizador nativo | `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate` e `\AU` | `apt-config dump` (`APT::Periodic`) + `50unattended-upgrades` | configuração do dnf automatic (a verificar) | `/Library/Preferences/com.apple.SoftwareUpdate` (auxiliar, não documentado) |
| Hardware | a definir: fabricante, modelo e BIOS no registro; serial e UUID pelo WMI (depois da Aula 3.1) | DMI em `/sys/class/dmi/id/` (serial e UUID exigem root) + `/proc/cpuinfo` + `/proc/meminfo` | idem | `system_profiler SPHardwareDataType -json` (identificador de modelo, serial, `platform_UUID`, `boot_rom_version`) + `sysctl -n hw.memsize hw.ncpu machdep.cpu.brand_string` |
| Identidade da instalação | `MachineGuid` em `HKLM\SOFTWARE\Microsoft\Cryptography` | `/etc/machine-id` | `/etc/machine-id` | a definir (Aula 4.3) |
| Identidade do hardware | `Win32_ComputerSystemProduct`: `UUID`, `IdentifyingNumber` (depois da Aula 3.1) | `product_uuid` e `product_serial` do DMI | idem | `platform_UUID` e `serial_number` |
| Nome da máquina | nome curto | nome configurado | nome configurado | três nomes no `scutil --get`: `ComputerName`, `LocalHostName` (curto) e `HostName` (pode ter domínio) |
| Antivírus ativo | `root/SecurityCenter2` `AntivirusProduct` (a confirmar) | fora de escopo | fora de escopo | fora de escopo |

## Regras de cada fonte, verificadas no Módulo 2

**Windows (chaves Uninstall)**
- Toda entrada com `DisplayName` é coletada; `SystemComponent=1` e entradas de atualização (`ParentKeyName` ou `ReleaseType`) são marcadas, e o filtro fica no servidor.
- Desde o Windows 8, atualizações do SO **não** se registram nas chaves Uninstall.
- `ProductCode` só em entradas MSI (`WindowsInstaller=1`) com chave em formato GUID.
- `InstallDate` fora do formato `AAAAMMDD` é descartado.
- Perfis de usuários sem sessão aberta não estão no `HKU` e ficam fora da v1.

**dpkg**
- Instalado = estado atual `i`, `W` ou `t`. `n` (não instalado) e `c` (só configuração) ficam fora.
- Estados `H`, `U`, `F` e a flag `R` indicam dpkg quebrado, que trava o apt: são reportados como erro.
- `h` no estado desejado = pacote travado (`held`).
- O pacote fonte é a unidade dos avisos do Ubuntu (ex.: fonte `c-ares`, binário `libcares2`).

**rpm**
- `(none)` vira vazio; versão no formato EVR, com época quando houver.
- Pseudo-pacotes `gpg-pubkey` (chaves importadas) são filtrados.
- O pacote fonte sai do nome do `.src.rpm`, quebrado da direita para a esquerda (ex.: fonte `openssl`, binário `openssl-libs`).

**Qual banco de pacotes consultar**
- Pelo `ID` e `ID_LIKE` do `os-release`, nunca pela presença do executável: um Ubuntu pode ter o `rpm` instalado.

**Hardware no Linux**
- Arquivo DMI inexistente (ARM, alguns ambientes virtuais) = campo vazio; permissão negada = erro parcial.
- Serial e UUID são enviados crus; a lista de valores genéricos ("To be filled by O.E.M.") fica no servidor.
- Em VMs VMware, o serial é derivado dos mesmos bytes do UUID, com os três primeiros campos em outra ordem (o SMBIOS grava esses campos em little-endian).

**macOS**
- Número vem do `sysctl`; o `system_profiler` formata memória e processadores como texto.
- Apps em `/Users/<nome>/` são do usuário (exceto `/Users/Shared`); apps em `/System/` e recibos `com.apple.*` são componentes do sistema.
- Fornecedor pelo certificado Developer ID, sem o Team ID; apps da Mac App Store não expõem o desenvolvedor na assinatura.
- `/Applications/Safari.app` é link simbólico para o cryptex de apps (`/System/Cryptexes/App/System/Applications`), e o `system_profiler` **não** lista esse app: a pasta do cryptex é lida diretamente.
- Recibo do `pkgutil` é evidência de instalação, não de estado atual: persiste depois de o conteúdo mudar (dezenas de recibos do XProtect convivem).

## Comparação de versões

| Esquema | Onde | Regras principais |
|---|---|---|
| `dpkg` | Ubuntu/Debian | `[época:]upstream[-revisão]`; época vence tudo; `~` ordena antes de tudo, até do fim; letras antes de símbolos; revisão ausente = `0` |
| `rpm` | Fedora/RHEL | `[época:]versão[-release]`; segmentos numéricos ou alfabéticos, separadores ignorados; numérico mais novo que alfabético; `~` antes de tudo; `^` depois da base e antes de um segmento a mais; release só comparada quando os dois lados têm |
| `dotted` | build.UBR do Windows, versão do macOS | números separados por pontos; parte ausente = 0; `DisplayVersion` do Windows (`25H2`) não é comparável |

As implementações foram conferidas contra `dpkg --compare-versions` e `rpm.vercmp`.

## Visões que não são fonte

| Visão | Por que não serve | Evidência no laboratório |
|---|---|---|
| `ProductName` no registro | Valor legado | "Windows 10 Pro" num Windows 11 25H2 (build 26200) |
| `Get-HotFix` / `Win32_QuickFixEngineering` | Só enxerga pacotes do CBS; `InstalledOn` é a data da consolidação no reboot | Drivers, ferramenta de remoção de malware e PowerShell 7 ausentes; datas 3 dias depois da instalação |
| Histórico do WUA como estado | Só registra o que o próprio WUA instalou | KBs aplicados por imagem ou DISM não aparecem |
| `Win32_Product` | Dispara verificação de consistência em todos os pacotes MSI | Proibido no projeto |
| Títulos de atualização | Localizados | "Atualização de Segurança" em pt-BR |
| `apt list` | Interface sem estabilidade para scripts, saída localizada | Aviso do próprio apt; "atualizável de" |
| Log do `unattended-upgrades` | Traduzido | "Pacotes que serão atualizados"; para histórico, o `dpkg.log` |
| `systemctl status unattended-upgrades` | A unit é o ajudante de desligamento; quem instala é o `apt-daily-upgrade.timer` | Serviço "ativo" sem ter instalado nada, até o timer agir |
| `rpm --json` | Correto, mas traz o cabeçalho inteiro (assinaturas, scripts, lista de arquivos) | Formato próprio com `--queryformat` é suficiente e ordens de grandeza menor |
| `system_profiler` para memória e CPU | Texto formatado ("64 GB", "proc 24:16:8:0") | Números crus pelo `sysctl` |
| `SPApplicationsDataType` sozinho | Omite os apps do cryptex sem acusar erro | Safari ausente |
| Recibos do `pkgutil` como estado atual | Persistem depois de o conteúdo mudar | Dezenas de versões do XProtect |
| Preferências de atualização automática do macOS | Configuração não é resultado | Instalação automática ligada e máquina cinco versões menores atrás |
| Versão do upstream citada em CVE | As distros fazem backport | Correção de CVE na mesma versão 1.34.6 com sufixo `ubuntu0.1` |
| Inventário feito de dentro de um container | Os arquivos são da imagem e o kernel é do host | "Debian 13" com kernel do WSL2 |

## Notas por SO

**Windows**
- Numa frota com WSUS, a busca online do WUA responde "o que o WSUS aprovou". O scan offline com `wsusscn2.cab` é obrigatório para a visão da Microsoft (RF-27).
- 24H2 e 25H2 compartilham a mesma linha de manutenção; o 25H2 chega por pacote de habilitação. O que mede o nível é o UBR.
- Entre a instalação da LCU e o reboot, a máquina está instalada, mas não efetiva.
- Consumidores da saída do agente no Windows precisam decodificar UTF-8.

**Ubuntu**
- Correções de segurança saem no pocket `-security` e também em `-updates`; sem metadados frescos do `-security`, não dá para classificá-las.
- Metadados vencidos produzem falso negativo. Relógio errado invalida os metadados.
- O `unattended-upgrades` aplica correções do `-security` sozinho, no horário sorteado pelo timer.
- Hipótese a confirmar: atualizações em fases (*phased updates*) explicam pacotes listados como atualizáveis que a simulação não instala.

**Fedora**
- O `dnf5` tem saída JSON (`--json`) na listagem e no detalhe dos advisories.
- O `buildtime` do JSON coincide com o `Issued` do detalhe. Só advisories com status `stable` contam.
- As CVEs aparecem apenas nos títulos das referências (tipo `bugzilla`); o mapeamento estruturado vem do catálogo do servidor.

**macOS**
- A atualização menor dentro da major e a atualização de major aparecem lado a lado no `softwareupdate --list`; `Action: restart` indica reboot.
- O plist de preferências do `softwareupdate` não é documentado: dado auxiliar, nunca fonte principal.
- O zsh interativo não aceita `#` como comentário sem `setopt interactivecomments`.

## Catálogos do servidor (Módulo 6)

O servidor baixa os catálogos e cruza com o inventário localmente: o inventário da frota nunca é enviado a uma API externa.

O servidor sincroniza todas as fontes sozinho, a cada `PATCHD_CATALOG_INTERVAL` (padrão `6h`; `0` desliga), com a primeira execução entre 1 e 5 minutos depois da partida. Uma trava no PostgreSQL (`pg_try_advisory_lock`) impede duas sincronizações ao mesmo tempo no mesmo banco, seja o agendamento, um `catalog sync` manual ou outra instância. Cada fonte de cada execução fica registrada em `catalog_runs` (90 dias); uma fonte sem sucesso há mais de 48 h aparece como atrasada no `catalog status` e gera aviso no `compliance` (Aula 6.6).

| Fonte | Endereço | Como a sincronização sabe o que mudou | Observações |
|---|---|---|---|
| MSRC (Windows) | `https://api.msrc.microsoft.com/cvrf/v3.0` | `CurrentReleaseDate` da lista `/updates` (a API responde `no-store`, sem ETag) | Documentos são revisados depois de publicados, inclusive meses antigos com KBs novos. Um documento pode sumir da lista por algumas horas (2026-Aug e 2026-Sep, em 06/10/2026): o guardado é mantido e o sumiço é avisado |
| Ubuntu (USN) | `https://storage.googleapis.com/osv-vulnerabilities/Ubuntu/` | `modified_id.csv` (data com nanossegundos, guardada como texto) | Só a série `USN-*` mede conformidade; `UBUNTU-CVE-*` inclui o que não tem correção. Versões com época, como vieram |
| Fedora | Bodhi (`https://bodhi.fedoraproject.org`), versões `F<n>` em `current` | `pushed_since` a partir da maior `date_pushed` guardada, menos 24 h (o `modified_since` ignora updates nunca editados); `-full` baixa tudo de novo | CVEs só nos títulos dos bugs de segurança, cortados com `...`: lista marcada como incompleta; uns 40% dos updates de segurança não citam CVE nenhuma. Builds pelo NVR do pacote fonte, com a época. Lento (~20 s por página de 100) e derruba conexões: cada pedido é repetido com espera crescente |
| Apple | `gdmf.apple.com/v2/pmv` (versões e builds publicados; só com a raiz "Apple Root CA", embutida e conferida pela impressão digital) e SOFA v2 (`sofafeed.macadmins.io`, versões de segurança com CVEs, exploração e KEV, e os modelos de Mac com as majors que cada um aceita) | SHA-256 do conteúdo reduzido e ordenado (gdmf: o corpo muda sem o conteúdo mudar) e `UpdateHash` (SOFA); cada fonte é substituída inteira quando muda, e uma resposta vazia é recusada | O `gdmf` lista versões sem suporte de segurança (do 11 ao 27) e datas de publicação no catálogo, não da versão. Uma versão pode ter dois builds (27.0.1). Melhorias em segundo plano têm `(a)` e build pré-requisito, e o SOFA não traz as CVEs delas. A Apple não publica política de suporte: o fim de uma major sai do próprio catálogo (Aula 6.4). O gdmf identifica os Macs pelo board ID; o SOFA e o agente, pelo identificador do modelo |
| CISA KEV (exploração) | `https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json` (espelho oficial: `cisagov/kev-data` no GitHub, branch `develop`) | `dateReleased` do próprio feed; o arquivo (1,8 MB) é substituído inteiro quando muda, e só se o `count` bater com os itens | Régua comum de exploração: a priorização considera explorada a CVE no KEV ou marcada pelo fabricante (o MSRC deixa de marcar CVEs exploradas depois do Patch Tuesday). No Fedora, sem CVEs no Bodhi, o update é ligado ao KEV por uma tabela curta de produto para pacote (`kev.FedoraRules`) e marcado como inferido |
| Programas de terceiros | Manifesto embutido (`internal/thirdparty/manifesto.json`) mais o local (`PATCHD_THIRDPARTY_MANIFEST`); versões de referência no Google (`versionhistory.googleapis.com`), na Mozilla (`product-details.mozilla.org`), no winget-pkgs (API do GitHub, 60 pedidos/h sem autenticação) e no Homebrew (`formulae.brew.sh`) | Nenhum sinal de mudança: cada sincronização pergunta a versão mais nova de cada fonte (uma vez por fonte); a falha mantém a última versão guardada | "Desatualizado" é estar abaixo da última versão publicada, não necessariamente vulnerável. O `DisplayName` traz versão e idioma, o `ProductCode` muda a cada versão e o `UpgradeCode` (estável) não está na chave Uninstall. O winget pode guardar só a versão atual (Chrome) e acompanha a linha mais nova (LibreOffice, PowerShell) |
