package api

const panelGuidanceTemplates = `
{{define "page-guide"}}{{with .PageGuide}}<details class="page-guide"><summary>Como usar esta página</summary><div class="page-guide-body"><h2>{{.Title}}</h2><p>{{.Summary}}</p><ol>{{range .Steps}}<li>{{.}}</li>{{end}}</ol><a href="/painel/ajuda#{{.ID}}">Abrir a ajuda completa →</a></div></details>{{end}}{{end}}

{{define "settings-navigation"}}<nav class="settings-navigation" aria-label="abas da configuração"><div class="abas">{{range .SettingsPrimary}}<a href="{{.Href}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</div><details class="settings-more"{{if .SettingsMoreOpen}} open{{end}}><summary>Mais configurações{{if .SettingsMoreOpen}} <span class="muted">· área selecionada abaixo</span>{{end}}</summary><div class="settings-more-links">{{range .SettingsMore}}<a href="{{.Href}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</div></details></nav>{{end}}

{{define "settings-start"}}<div class="settings-start"><h2>O que você quer configurar?</h2><p class="muted">Escolha uma tarefa. Os dados técnicos do servidor ficam em Diagnóstico e versão.</p><div class="settings-task-grid">
<a href="/painel/configuracao?aba=agente"><strong>Adicionar computadores</strong><span>Versão do agente, download e tokens de registro.</span></a>
<a href="/painel/configuracao?aba=localizacoes"><strong>Organizar prédios e salas</strong><span>Identifique os andares e confira as regras de localização.</span></a>
<a href="/painel/configuracao?aba=acesso"><strong>Gerenciar usuários e acesso</strong><span>Contas, perfis e formas de entrar no painel.</span></a>
{{if .ParamsOn}}<a href="/painel/configuracao?aba=parametros"><strong>Ajustar preferências</strong><span>Fuso, comunicação dos agentes e notificações.</span></a>{{end}}
<a href="/painel/configuracao?aba=autoatendimento"><strong>Preparar o catálogo da Central</strong><span>Escolha os aplicativos que os usuários podem pedir.</span></a>
{{if .HealthOn}}<a href="/painel/configuracao?aba=saude"><strong>Ver a saúde do servidor</strong><span>Investigue recursos, lentidão e recomendações.</span></a>{{end}}
</div></div>{{end}}

{{define "ajuda"}}{{template "topo" .}}
<div class="help-heading"><span class="ux-eyebrow">Guia do painel</span><h1>Como podemos ajudar?</h1><p>Encontre uma tarefa e siga os passos. Esta ajuda acompanha a versão instalada do servidor.</p></div>
<form class="help-search" method="get" action="/painel/ajuda" role="search"><label for="help-query">O que você precisa fazer?</label><div><input id="help-query" type="search" name="q" value="{{.Query}}" maxlength="100" placeholder="Ex.: instalar programas, reiniciar, jobs, inventário"><button type="submit">Buscar ajuda</button>{{if .Query}}<a href="/painel/ajuda">Limpar busca</a>{{end}}</div></form>
{{if .Query}}<p class="muted" role="status">{{len .Tasks}} assunto(s) encontrado(s) para “{{.Query}}”.</p>{{end}}
{{if .Tasks}}<h2 class="sr">Assuntos da ajuda</h2>
<div class="help-tasks">{{range .Tasks}}<details class="help-task"{{if $.Query}} open{{end}}><summary><span class="help-task-title">{{.Title}}</span><span class="help-task-summary">{{.Summary}}</span></summary><div class="help-task-body" id="{{.ID}}"><ol>{{range .Steps}}<li>{{.}}</li>{{end}}</ol><div class="help-actions">{{range .Links}}<a class="btn" href="{{.Href}}">{{.Label}} →</a>{{end}}</div></div></details>{{end}}</div>
{{else}}<div class="vazio"><strong>Nenhum assunto encontrado.</strong>Tente uma palavra mais simples, como computador, atualização ou acesso. <a href="/painel/ajuda">Ver todos os assuntos</a></div>{{end}}
<aside class="help-support"><h2>Ainda precisa de ajuda?</h2><p>Informe à equipe de TI a tela, o nome da tarefa e a mensagem de erro. Em uma execução, inclua o número e o horário. Não envie senhas, tokens, credenciais ou chaves.</p><p><b>Seu perfil: {{.RoleLabel}}.</b>{{if not .Admin}} Você pode consultar as informações. Aprovar, instalar ou alterar configurações exige um administrador.{{end}}</p></aside>
{{template "fim"}}{{end}}
`
