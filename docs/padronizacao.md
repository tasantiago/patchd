# Padronização de computadores

A área **Automação › Padronização** define o que cada computador precisa ter. Um padrão básico pode atender toda a frota e se somar a grupos como Financeiro e RH. Cada grupo pode conter aplicativos e atalhos. A configuração começa como rascunho; marque **Ativo** para o agente começar a aplicar.

## Preparar servidor e agentes

Atualize o servidor e publique agentes desta versão para Windows, Linux e macOS, conforme o fluxo de geração e publicação de versões do [README](../README.md). A migração 0053 é automática no início do servidor. Preserve a chave de jobs e publique agentes com sua chave pública: as instalações continuam usando os jobs assinados do patchd.

O serviço precisa rodar como SYSTEM no Windows ou root no Linux/macOS. Para aplicativos de grupos de computadores, basta o serviço ativo. Para grupos do usuário e atalhos, mantenha também o aplicativo da sessão habilitado (`-user-app=true`, padrão).

No Windows, a Central inicia no logon. No Linux com sessão gráfica XDG, o serviço configura `/etc/xdg/autostart/patchd-standardization.desktop`. No macOS, configura `/Library/LaunchAgents/com.patchd.standardization.plist`. Esses dois executam `patchd-agent app -no-tray`. O canal entre sessão e serviço usa named pipe no Windows e `/var/run/patchd/user.sock` no Linux/macOS; o serviço autentica a identidade pelo sistema operacional.

Após atualizar, reinicie o serviço. No Linux, reinstale a unidade com `patchd-agent service install` usando as mesmas opções da instalação original e recarregue o systemd se a unidade antiga ainda restringir `/usr`, `/opt` e `/etc` a leitura. A unidade atual permite a escrita necessária aos instaladores nesses locais; `/home` continua somente leitura para o serviço. Faça novo logon para carregar a configuração de início da sessão. Para testar na sessão atual, execute `patchd-agent app -no-tray`, sem sudo. A primeira sincronização ocorre após 15 segundos e se repete a cada cinco minutos.

## Criar o padrão básico e os setores

1. Abra **Padronização** como administrador e clique em **Criar padrão básico**.
2. Escolha os aplicativos e atalhos comuns. Marque **Padrão básico para todos os computadores**.
3. Salve, confira os destinos e ative o grupo quando estiver pronto.
4. Crie **Financeiro**, selecione seus computadores, seus grupos do AD ou ambos e defina os aplicativos e atalhos do setor. Repita para **RH**.

Os destinos específicos usam **OU**: um computador selecionado recebe o grupo; um usuário que pertence a um dos grupos informados também recebe. Todos os padrões aplicáveis se somam. O mesmo aplicativo não é instalado novamente por pertencer a dois grupos. Nenhum programa é desinstalado quando um computador sai do grupo.

No Windows, grupos do AD vêm do token do logon, incluindo os grupos habilitados e resolvidos pelo domínio. Em Linux e macOS, o computador precisa estar integrado ao domínio e resolver os grupos do usuário por NSS/serviços de diretório. Use o nome exibido por `patchd-agent selfservice whoami -groups`. Um nome qualificado, como `CORP\Financeiro`, exige esse domínio. Um nome sem domínio corresponde ao grupo local ou à parte final de `DOMINIO\grupo`. Nomes no formato `grupo@dominio` devem ser informados por inteiro. Atualize a sessão após mudanças no AD.

## Aplicativos do catálogo Windows

Marque os aplicativos desejados no catálogo. Antes de ativar o padrão, baixe os instaladores e aprove as versões em **Patch › Terceiros**. O agente instala aplicativos ausentes e atualiza até a versão aprovada compatível mais recente; versões superiores são mantidas.

A avaliação respeita arquitetura, MSI/EXE, instalações duplicadas e instalações por usuário. Uma situação incompatível fica bloqueada com o motivo. Os arquivos são conferidos pelo SHA-256 e os jobs continuam usando a assinatura e a verificação de instaladores do fluxo existente.

## Aplicativos por script em Windows, Linux e macOS

Use essa opção para aplicativos internos e para instaladores Linux/macOS:

1. Cadastre um ZIP com seus arquivos ou um script avulso na área **Scripts**. Salve **Run**, parâmetros e timeout. Linux/macOS usam `.sh`; Windows usa um dos interpretadores Windows disponíveis no cadastro.
2. Prepare um instalador silencioso, idempotente, sem interação, que instale para a máquina. O script roda como serviço, sem a sessão gráfica do usuário. Não use Homebrew como root; no macOS prefira um instalador `.pkg` adequado ou outro método que suporte esse contexto.
3. Em **Padronização**, escolha o pacote, o sistema e o **nome exato no inventário**. Exemplos: `firefox` no Linux, `Google Chrome` no macOS, ou o identificador de recibo apresentado no inventário. Consulte uma instalação de teste para confirmar esse nome.
4. Salve o grupo. Salvar fixa e aprova seu conteúdo e parâmetros para os destinos escolhidos. Alterar Run na área Scripts não altera os grupos existentes: salve novamente o grupo para adotar a nova configuração. Para mudar os arquivos, envie outro pacote e selecione-o.

Essa modalidade verifica **presença**, sem impor uma versão. O agente executa o instalador apenas quando o nome estiver ausente do inventário da máquina. Uma falha na coleta de software bloqueia novas instalações. Concluir o script não basta para mostrar conformidade: o próximo inventário precisa conter o aplicativo. Se isso não acontecer, a tela mostra o problema; confira o nome de detecção e o script antes da próxima tentativa, espaçada por seis horas.

O script deve tratar diferenças entre distribuições e arquiteturas. Por exemplo, um script Linux pode identificar `apt` ou `dnf` e instalar o pacote apropriado, com tratamento de erro. O patchd não transforma um instalador Windows em instalador Linux/macOS.

## Atalhos e ícones

Cada atalho informa nome, tipo, destino, sistema e um ícone padrão. Sites aceitam apenas HTTP/HTTPS, sem credenciais na URL. Aplicativos não recebem argumentos de execução no atalho.

| Sistema | Destinos | Formato e ícones |
| --- | --- | --- |
| Windows | `.exe`, pasta absoluta/rede ou variável de perfil/programas permitida | `.lnk` e `.url`; ícones do sistema ou do próprio aplicativo |
| Linux | aplicativo ou pasta com caminho absoluto ou `~/` | `.desktop`; ícones do tema para site, aplicativo, pasta e documento |
| macOS | aplicativo `.app`/arquivo ou pasta com caminho absoluto ou `~/` | `.webloc` e `.fileloc`; ícones padrão do Finder |

Sites podem valer para todos os sistemas. Para aplicativos e pastas, o agente filtra caminhos incompatíveis. Para usar o mesmo nome com destinos diferentes por sistema, crie uma linha para cada sistema. Upload de imagens de ícone não está incluído.

Os atalhos são escritos pelo usuário, na própria Área de Trabalho. No Linux, respeitam `XDG_DESKTOP_DIR`; a sessão gráfica pode exigir permitir a execução ou confiar no launcher conforme suas regras. O patchd preserva arquivos pessoais e atalhos alterados pelo usuário. Só remove atalhos que ele criou e cujo conteúdo continua intacto, quando deixam de fazer parte do padrão. Sem Área de Trabalho disponível, a sessão informa falha e tenta novamente depois.

Atalhos com o mesmo nome e destinos diferentes em grupos sobrepostos ficam bloqueados. O acompanhamento mostra o conflito para o administrador corrigir a configuração. Instaladores diferentes para o mesmo nome de aplicativo por script também são bloqueados.

## Acompanhar e corrigir

Aplicativos de padrões por computador são avaliados no check-in. Os grupos do usuário e os atalhos dependem da sessão ativa. São criados até cinco jobs por avaliação, respeitando a fila de instalações já aberta. Depois da execução, o agente envia novo inventário e antecipa outro check-in. Tentativas sem sucesso são espaçadas por seis horas.

A tabela mostra até 500 avaliações recentes por computador e conta: aplicativos em dia, agendados, em andamento, aguardando ou bloqueados. Os aplicativos são confirmados pelo inventário. Atalhos mostram o último relato da sessão, com sua data; isso não é uma comprovação independente de conformidade de toda a frota.

Desativar ou excluir um grupo interrompe novas aplicações desse grupo, mas não desfaz jobs emitidos. Cancele jobs ainda pendentes em **Jobs**, se necessário. Aplicativos instalados permanecem; atalhos gerenciados são revistos na próxima sincronização do usuário.

Por grupo: até 10.000 computadores, 20 grupos de usuários, 64 aplicativos somando catálogo e scripts e 24 atalhos. A união de grupos também limita o plano a 64 aplicativos e 24 atalhos e informa quando algum limite é alcançado. Edições simultâneas exigem recarregar a revisão atual. Ações administrativas e criação de jobs ficam na auditoria.
