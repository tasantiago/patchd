#!/usr/bin/env python3
"""Atualiza páginas operacionais de um clone da wiki; não autentica nem faz push."""

import argparse
import re
from pathlib import Path
from urllib.parse import quote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
REPO = "https://github.com/tasantiago/patchd"
PAGES = {
    "Manual-do-usuario": "docs/manual-do-usuario.md",
    "Usuario-03-Utilizacao": "docs/manual-do-usuario.md",
    "Atualizacoes-recentes": "docs/atualizacoes-recentes.md",
    "Comunicacao-e-horario": "docs/comunicacao-e-horario.md",
    "Canal-em-tempo-real": "docs/canal-tempo-real.md",
    "Padronizacao": "docs/padronizacao.md",
    "Rele-na-nuvem": "docs/rele.md",
    "Saude-do-servidor": "docs/saude-servidor.md",
    "Monitoramento-e-alertas": "docs/monitoramento.md",
    "Pagina-inicial": "docs/dashboard.md",
    "Pacotes-de-scripts": "docs/pacotes-de-scripts.md",
    "Ligar-pela-rede": "docs/wake-on-lan.md",
    "Operacao-em-producao": "docs/operacao.md",
    "Instalacao-com-Podman": "docs/podman.md",
}

CHAPTERS = {
    "Usuario-01-Instalacao": """# Manual — instalação do servidor e dos agentes

Os roteiros atuais instalam uma VM nova sem copiar arquivos do contêiner de desenvolvimento. Escolha [Docker no Ubuntu ou Podman no RHEL](https://github.com/tasantiago/patchd/blob/main/README.md#instalar-um-servidor-novo-do-zero).

1. Prepare a VM, o DNS, a saída de rede e o armazenamento.
2. Clone a revisão aprovada e configure `deploy/.env`.
3. Compile a imagem oficial do servidor e gere a chave de jobs de uma instalação nova. Em migrações, preserve a chave atual.
4. Prepare TLS, os segredos, PostgreSQL e o primeiro administrador.
5. Compile e publique agentes assinados com as chaves correspondentes e registre um piloto.
6. Valide coleta, comunicação, permissões e o backup antes de ampliar o parque.

Os comandos completos, os caminhos e as diferenças de permissões entre Docker e Podman ficam no README. O [build de agentes sem dev](https://github.com/tasantiago/patchd/blob/main/docs/build-agentes-sem-dev.md) explica assinatura e publicação. [Próximo: configuração](Usuario-02-Configuracao).
""",
    "Usuario-02-Configuracao": """# Manual — configuração inicial

O painel atual possui os perfis **leitura** e **admin**. Login local, AD/LDAPS e OIDC/RHBK usam as permissões documentadas na [segurança](https://github.com/tasantiago/patchd/blob/main/docs/seguranca.md). Os grupos de implantação organizam campanhas; não devem ser tratados como uma permissão de leitura por grupo.

| Configuração | Onde e efeito |
| --- | --- |
| Nome, fuso e retenção | Configuração › Parâmetros; valores salvos na tela prevalecem sobre seus valores iniciais no ambiente. |
| Frequência dos agentes | Agentes › Intervalo de check-in; agentes compatíveis adotam no próximo contato. |
| Jobs em segundos | Agentes › Canal em tempo real; confira a conexão no Resumo da máquina. |
| Notebooks externos | Agentes › Relé para fora da rede, junto com a preparação do túnel e da porta só de agentes. |
| Fonte reserva do Windows | Desligada por padrão; exige aprovação no patchd e respeita os bloqueios da GPO. |
| Grupos e janelas | Automação › Grupos de implantação; os grupos numéricos continuam no comando `ring`. |
| Aplicativos e atalhos por setor | Automação › Padronização; os padrões podem se somar por computador e grupo do AD. |

Para registrar máquinas, use os fluxos de instalação e tokens do README; o enrollment é `POST /api/v1/enroll`, atendido na entrada principal. A porta só de agentes não cadastra máquinas novas.

Veja [operação](https://github.com/tasantiago/patchd/blob/main/docs/operacao.md), [portas e horário](Comunicacao-e-horario) e [manual atualizado](Manual-do-usuario). [Próximo: utilização](Usuario-03-Utilizacao).
""",
    "Usuario-04-Manutencao": """# Manual — atualização, backup e manutenção

Para código novo, atualize primeiro o servidor e depois publique agentes assinados quando a função depende deles. O [roteiro de versões](https://github.com/tasantiago/patchd/blob/main/README.md#gerar-e-publicar-uma-nova-versão-do-servidor-e-do-agente) inclui Docker e Podman. Preserve as chaves existentes e acompanhe a versão realmente reportada por cada máquina.

O `deploy/atualizar.sh` constrói a imagem, faz a cópia prevista, troca e espera o healthcheck. Migrações são automáticas; voltar a uma imagem que não conhece a migração nova pode exigir restaurar o backup de antes. O [guia de operação](https://github.com/tasantiago/patchd/blob/main/docs/operacao.md) descreve backup, ensaio de restauração, retenção e reversão.

Porta e fuso são configuração: [aplique sem reconstruir a imagem](Comunicacao-e-horario). Uma troca de porta exige manter a entrada antiga para que os agentes recebam o job de migração. Para o relé, atualize e configure os agentes enquanto ainda há contato direto.

Em Configuração › Saúde, acompanhe dimensionamento e banco. Em Alertas, reconhecimento, fechamento manual e limpeza de temporários são ações diferentes e têm conferência. Consulte [monitoramento](Monitoramento-e-alertas) e [novidades](Atualizacoes-recentes). [Próximo: diagnóstico](Usuario-05-Diagnostico).
""",
    "Usuario-05-Diagnostico": """# Manual — diagnóstico

1. Confira DNS, TCP e TLS para o hostname e a porta da URL do agente. Teste `/healthz` com a CA confiável; não desative a validação como solução para o erro.
2. Confira o serviço, sua versão, o último check-in e o `request_id` do erro. A imagem do servidor é `scratch` e não tem shell nem `date`; o relógio é investigado no host e os logs podem usar UTC ou o fuso local.
3. Um check-in HTTP 200 prova comunicação naquele instante. Analise também as datas e erros de inventário/busca antes de concluir conformidade.
4. Jobs pendentes dependem de comunicação, validade e janela; os novos agentes podem ser avisados pelo canal em tempo real. Recusa de assinatura ou agente incompatível exige corrigir a causa.
5. Campanha pausada pode ser reenviada depois da correção. Retomar aceita as falhas da etapa; Reenviar refaz o que ainda precisa. Confira os motivos e os novos resultados.
6. Para máquinas externas, teste o túnel, o HAProxy, a porta só de agentes e o relé aprendido. O painel é recusado na porta só de agentes por projeto.

Exemplos Podman, como o usuário do serviço:

```bash
systemctl --user status patchd-server --no-pager
journalctl --user -u patchd-server -n 50 --no-pager
podman logs --tail 50 --timestamps patchd-server
podman exec patchd-server /patchd-server job list -n 100
```

Reúna versão, horário, operação e erro sem incluir credenciais, chaves ou inventário real em publicações. Veja [comunicação e fuso](Comunicacao-e-horario), [saúde do servidor](Saude-do-servidor) e [manual](Manual-do-usuario).
""",
}


def absolute_links(content, source):
    """Converte links Markdown relativos; blocos de código permanecem intactos."""
    def replace(match):
        target = match.group(2)
        if urlsplit(target).scheme or target.startswith(("#", "//")):
            return match.group(0)
        path, sep, anchor = target.partition("#")
        resolved = (ROOT / source).parent.joinpath(path).resolve().relative_to(ROOT)
        url = REPO + "/blob/main/" + quote(resolved.as_posix(), safe="/")
        return match.group(1) + url + (sep + anchor if sep else "") + ")"

    pieces = re.split(r"(```.*?```|~~~.*?~~~)", content, flags=re.S)
    for index in range(0, len(pieces), 2):
        pieces[index] = re.sub(r"(\[[^\]\n]*\]\()([^\s)]+)\)", replace, pieces[index])
    return "".join(pieces)


def managed(content, block):
    start, end = "<!-- patchd-operacao-atual:inicio -->", "<!-- patchd-operacao-atual:fim -->"
    content = re.sub(re.escape(start) + r".*?" + re.escape(end) + r"\n*", "", content, flags=re.S)
    return start + "\n" + block.strip() + "\n" + end + "\n\n" + content.lstrip()


def export(destination):
    destination.mkdir(parents=True, exist_ok=True)
    written = []
    for name, source in PAGES.items():
        content = absolute_links((ROOT / source).read_text(encoding="utf-8"), source)
        notice = f"\n\nFonte versionada: [{source}]({REPO}/blob/main/{source}).\n"
        (destination / (name + ".md")).write_text(content.rstrip() + notice, encoding="utf-8")
        written.append(name)
    for name, content in CHAPTERS.items():
        (destination / (name + ".md")).write_text(content.strip() + "\n", encoding="utf-8")
        written.append(name)
    navigation = """## Operação atual — revisão de 09/11/2026

- [Manual atualizado](Manual-do-usuario) e [mudanças recentes](Atualizacoes-recentes)
- [Instalação do zero](Usuario-01-Instalacao) e [Podman](Instalacao-com-Podman)
- [Portas e horário](Comunicacao-e-horario), [canal e perguntas](Canal-em-tempo-real)
- [Relé na nuvem](Rele-na-nuvem), [padronização](Padronizacao)
- [Alertas e limpeza](Monitoramento-e-alertas), [saúde do servidor](Saude-do-servidor)
- [Dashboards](Pagina-inicial), [scripts](Pacotes-de-scripts), [ligar pela rede](Ligar-pela-rede)
- [Operação detalhada](Operacao-em-producao) e [índice versionado](https://github.com/tasantiago/patchd/blob/main/docs/README.md)

As trilhas abaixo mantêm seu conteúdo de formação. Para procedimentos da versão atual, use os guias acima e confira o servidor/agente instalado.
"""
    for name in ("Home", "_Sidebar"):
        path = destination / (name + ".md")
        content = path.read_text(encoding="utf-8") if path.exists() else "# patchd\n"
        if name == "Home":
            content = re.sub(r"(?m)^> \*\*Estado desta Wiki:\*\*.*$", "> **Estado desta Wiki:** navegação operacional revisada em 09/11/2026 contra a main; as trilhas de formação têm seus próprios contextos históricos. Confira os guias e a versão instalada.", content)
            content = content.replace("4. Sem execução arbitrária de shell ou comando remoto como funcionalidade de jobs.", "4. Operações por jobs tipados e assinados; pacotes de scripts são cadastrados e autorizados pelo administrador, conforme o guia próprio.")
        path.write_text(managed(content, navigation), encoding="utf-8")
        written.append(name)
    print("Páginas preparadas:", ", ".join(written))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--destino", type=Path, required=True, help="pasta de um clone da wiki ou pasta para preparar os arquivos")
    export(parser.parse_args().destino.resolve())
