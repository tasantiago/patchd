# Fontes de verdade por SO

Versão 0.1, consolidada na Aula 0.3 a partir de saídas reais coletadas no laboratório.

Uma **fonte de verdade** é o lugar onde o próprio SO guarda a informação que ele usa para decidir. Tudo o que apenas formata essa informação é uma **visão**, e visões não entram no inventário.

Critérios de escolha, em ordem:
1. Autoritativa: o SO decide com base nela.
2. Estruturada: registro, COM/WMI, JSON ou plist antes de texto.
3. Independente de idioma.
4. Legível sem rede.
5. Estável entre versões do SO.

Quando a melhor fonte falha, o agente usa a próxima e registra qual usou (P-03).

## Quadro principal

| Dado | Windows | Ubuntu | Fedora | macOS |
|---|---|---|---|---|
| Versão do SO | Registro `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`: `CurrentBuild`, `UBR`, `DisplayVersion`, `EditionID`. Windows 11 = `CurrentBuild` ≥ 22000 | `/etc/os-release`: `VERSION_ID`, `VERSION_CODENAME` | `/etc/os-release` | `/System/Library/CoreServices/SystemVersion.plist` via `plutil -convert json`: `ProductVersion`, `ProductBuildVersion` |
| Nível de patch | `UBR` | Versão de cada pacote | NEVRA de cada pacote (com epoch) | Versão + build do SO, mais componentes com atualização própria (Safari) |
| Software instalado | Chaves Uninstall: 64 bits, `WOW6432Node`, HKU | `dpkg-query -W -f` com formato próprio | `rpm -qa --queryformat` | `system_profiler SPApplicationsDataType -json` + `pkgutil --pkgs` |
| O que falta | WUA `IUpdateSearcher` online (reflete aprovações do WSUS) e offline com `wsusscn2.cab` (reflete publicações da Microsoft) | `LC_ALL=C apt-get -s dist-upgrade` (linhas `Inst`) + `pro security-status --format json` | `dnf advisory list --security --json` | `softwareupdate --list` (texto; amostra registrada) |
| Histórico (auditoria, não estado) | WUA `QueryHistory` | a definir (Aula 2.3) | a definir (Aula 2.3) | `system_profiler SPInstallHistoryDataType -json` |
| Reboot pendente | `Microsoft.Update.SystemInfo` `RebootRequired`; chaves `Component Based Servicing\RebootPending` e `WindowsUpdate\Auto Update\RebootRequired` | `/var/run/reboot-required` + `.pkgs`; kernel em execução × mais novo instalado | Kernel em execução × mais novo instalado; demais fontes a verificar | a definir (Aula 3.6) |
| Atualizador nativo | `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate` e `\AU` | `apt-config dump` (`APT::Periodic`) + `50unattended-upgrades` | Configuração do dnf automatic (a verificar) | `/Library/Preferences/com.apple.SoftwareUpdate` (auxiliar) |
| Identidade da instalação | `MachineGuid` em `HKLM\SOFTWARE\Microsoft\Cryptography` | `/etc/machine-id` | `/etc/machine-id` | a definir (Aula 4.3) |
| Identidade do hardware | `Win32_ComputerSystemProduct`: `UUID`, `IdentifyingNumber` | `/sys/class/dmi/id/product_uuid` (exige root) | `/sys/class/dmi/id/product_uuid` (exige root) | `system_profiler SPHardwareDataType -json`: `platform_UUID`, `serial_number` |
| Antivírus ativo | `root/SecurityCenter2` `AntivirusProduct` (a confirmar) | fora de escopo | fora de escopo | fora de escopo |

## Visões que não são fonte

| Visão | Por que não serve | Evidência no laboratório |
|---|---|---|
| `ProductName` no registro | Valor legado | Mostrou "Windows 10 Pro" num Windows 11 25H2 (build 26200) |
| `Get-HotFix` / `Win32_QuickFixEngineering` | Só enxerga pacotes do CBS; `InstalledOn` é a data da consolidação no reboot | Drivers, ferramenta de remoção de malware e PowerShell 7 ausentes; datas 3 dias depois da instalação |
| Histórico do WUA como estado | Só registra o que o próprio WUA instalou | KBs aplicados por imagem ou DISM não aparecem |
| `Win32_Product` | Dispara verificação de consistência em todos os pacotes MSI | Proibido no projeto |
| Títulos de atualização | Localizados | "Atualização de Segurança" em pt-BR |
| `apt list` | Interface sem estabilidade para scripts, saída localizada | Aviso do próprio apt; "atualizável de" |
| `systemctl status unattended-upgrades` | A unit é o ajudante de desligamento; quem instala é o `apt-daily-upgrade.timer` | Serviço "ativo" sem nunca ter instalado nada |
| Preferências de atualização automática do macOS | Configuração não é resultado | Instalação automática ligada e máquina cinco versões menores atrás |
| Versão do upstream citada em CVE | As distros fazem backport | Correção de CVE na mesma versão 1.34.6 com sufixo `ubuntu0.1` |

## Notas por SO

**Windows**
- Numa frota com WSUS, a busca online do WUA responde "o que o WSUS aprovou". O scan offline com `wsusscn2.cab` é obrigatório para a visão da Microsoft (RF-27).
- 24H2 e 25H2 compartilham a mesma linha de manutenção; o 25H2 chega por pacote de habilitação. O que mede o nível é o UBR.
- Entre a instalação da LCU e o reboot, a máquina está instalada, mas não efetiva.

**Ubuntu**
- Correções de segurança saem no pocket `-security` e também em `-updates`; sem metadados frescos do `-security`, não dá para classificá-las.
- Metadados vencidos produzem falso negativo (0 atualizações de segurança). Relógio errado invalida os metadados.
- Hipótese a confirmar: atualizações em fases (*phased updates*) explicam pacotes listados como atualizáveis que a simulação não instala.

**Fedora**
- O `dnf5` publica os advisories nos metadados do repositório e tem saída JSON (`--json`) na listagem e no detalhe.
- O campo `buildtime` do JSON coincide com o `Issued` do detalhe. Só advisories com status `stable` contam.
- As CVEs aparecem apenas nos títulos das referências (tipo `bugzilla`); o mapeamento estruturado vem do catálogo do servidor.

**macOS**
- A atualização menor dentro da major e a atualização de major aparecem lado a lado no `softwareupdate --list`; `Action: restart` indica reboot.
- Apps arrastados para `/Applications` não deixam recibo no `pkgutil`.
- O plist de preferências do `softwareupdate` traz datas de primeira oferta e a lista de atualizações recomendadas, mas não é documentado: dado auxiliar, nunca fonte principal.
- O zsh interativo não aceita `#` como comentário sem `setopt interactivecomments`.
