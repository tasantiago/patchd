package api

import "html/template"

// Os templates de Automação › Prédios e implantação (deployment.go), no padrão visual de
// Configuração › Localizações: um cartão recolhido por prédio, com o ícone e os indicadores.

const deployBuildingIcon = `<span class="location-building-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 21V5l8-3v19M12 8h8v13M2 21h20M8 7v1M8 11v1M8 15v1M16 12v1M16 16v1"/></svg></span>`

const deployChevron = `<svg class="location-chevron" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>`

var _ = template.Must(pages.New("aneis").Parse(`{{template "topo" .}}
<h1>Automação <span class="muted">› Prédios e implantação</span></h1>
{{template "automacao-abas" "aneis"}}
<div class="ux-locations ux-deploy">
<header class="ux-hero"><div><span class="ux-eyebrow">Implantação por localização</span><h2>Prédios e implantação</h2><p>Cada prédio do patrimônio tem a sua janela de manutenção, que vale para todos os andares dele. A campanha escolhe os prédios e andares e, se quiser, um piloto.</p></div>
<div class="ux-hero-side"><span class="ux-status">{{.NAll}} prédio(s)</span>{{if .Admin}}<a class="btn" href="{{.BatchHref}}">Janelas em lote</a>{{end}}<a class="btn sec" href="/painel/configuracao?aba=localizacoes">Configurar andares</a></div></header>
<div class="deployment-summary"><span><b>{{.Total}}</b> equipamento(s)</span><span><b>{{.NWith}}</b> prédio(s) com janela ativa</span><span><b>{{.NNoWin}}</b> sem janela</span><span><b>{{.Unlocated}}</b> sem localização identificada</span></div>
{{if .Legacy}}<p class="aviso">{{.Legacy}} prédio(s) sem janela podem receber, sem ambiguidade, a janela do grupo de implantação antigo das máquinas deles. <a href="/painel/grupos-implantacao/janelas-legadas">Conferir e aplicar</a>{{if .LegacyAmbiguous}} · {{.LegacyAmbiguous}} ambíguo(s), para configurar à mão{{end}}</p>{{else if .LegacyAmbiguous}}<p class="aviso">{{.LegacyAmbiguous}} prédio(s) têm máquinas em grupos antigos com janelas diferentes: configure a janela deles à mão. <a href="/painel/grupos-implantacao/janelas-legadas">Ver quais</a></p>{{end}}
<form class="busca deploy-filtro" method="get" action="/painel/grupos-implantacao"><input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Pesquisar prédio" aria-label="pesquisar prédio">
<select name="janela" aria-label="janela de manutenção"><option value="">Todos os prédios</option><option value="com" {{if eq .Filter "com"}}selected{{end}}>Com janela ativa</option><option value="sem" {{if eq .Filter "sem"}}selected{{end}}>Sem janela ou desativada</option></select>
<button type="submit">Filtrar</button>{{if or .Q .Filter}} <a href="/painel/grupos-implantacao">Limpar</a>{{end}}</form>
{{if .Buildings}}<p class="locations-list-hint">{{if or .Q .Filter}}{{.Shown}} de {{.NAll}} prédio(s). {{end}}Abra um prédio para consultar ou alterar a janela.</p><div class="locations-list">{{range .Buildings}}<details class="lugar location-building" id="{{.Anchor}}"><summary>
` + deployBuildingIcon + `
<span class="location-building-title"><b>{{localNome .Name}}</b><small>{{.Machines}} equipamento(s) · {{.FloorsWithMachines}} andar(es) identificado(s){{if .WindowOn}} · próxima janela {{.Next}}{{end}}</small></span>
<span class="location-building-stats"><span class="location-stat {{.WindowClass}}">{{.WindowStateText}}</span>{{if .Unfloored}}<span class="location-stat">{{.Unfloored}} sem andar</span>{{end}}{{if .Conflicts}}<span class="location-stat location-stat-danger">{{.Conflicts}} em conflito</span>{{end}}{{if not .Present}}<span class="location-stat">fora do patrimônio atual</span>{{end}}{{if not .Machines}}<span class="location-stat">sem equipamentos</span>{{end}}</span>
` + deployChevron + `</summary>
<div class="location-content">
<dl class="deploy-dados"><dt>Equipamentos</dt><dd>{{.Machines}}</dd><dt>Andares</dt><dd>{{range $i, $f := .Floors}}{{if $i}} · {{end}}{{$f.Label}} <span class="muted">({{$f.Machines}})</span>{{else}}nenhum identificado{{end}}</dd>
<dt>Sem andar identificado</dt><dd>{{.Unfloored}}</dd><dt>Com conflito de andar</dt><dd>{{.Conflicts}}</dd>
<dt>Janela</dt><dd>{{if .HasWindow}}{{.WindowText}}{{if not .WindowOn}} <b>(desativada)</b>{{end}} <small class="muted">{{.WindowBy}}</small>{{else}}sem janela{{end}}</dd>
<dt>Próxima janela</dt><dd>{{if .WindowOn}}{{.Next}}{{else}}—{{end}}</dd>{{if .RenamedFrom}}<dt>Nome anterior</dt><dd>{{.RenamedFrom}}</dd>{{end}}</dl>
<p class="deploy-links"><a href="{{.InvHref}}">Inventário do prédio →</a><a href="{{.LocHref}}">Regras de andar →</a></p>
{{if $.Admin}}<form class="location-editor deploy-janela" method="post" action="/painel/grupos-implantacao/predios/janela"><input type="hidden" name="predio" value="{{.ID}}">
<div class="location-section-heading"><h3>Janela de manutenção</h3><small>Vale para todos os andares do prédio. Término antes do início atravessa a meia-noite (ex.: 22:00 a 04:00).</small></div>
<fieldset class="deploy-dias"><legend>Dias da semana</legend>{{range .Days}}<label><input type="checkbox" name="dia" value="{{.Value}}" {{if .Checked}}checked{{end}}> {{.Label}}</label>{{end}}</fieldset>
<div class="location-fields deploy-horas"><label><span>Início</span><input type="time" name="inicio" value="{{.Start}}" required></label><label><span>Término</span><input type="time" name="fim" value="{{.End}}" required></label><label><span>Fuso horário</span><input name="fuso" value="{{.TZ}}" maxlength="64" required spellcheck="false"></label></div>
<div class="deploy-botoes"><button type="submit" name="op" value="gravar">Gravar janela</button>{{if .HasWindow}}{{if .WindowOn}}<button class="sec" type="submit" name="op" value="desativar" formnovalidate>Desativar</button>{{else}}<button class="sec" type="submit" name="op" value="ativar" formnovalidate>Ativar</button>{{end}}<button class="perigo" type="submit" name="op" value="remover" formnovalidate>Remover</button>{{end}}</div></form>{{end}}
</div></details>{{end}}</div>
{{else if or .Q .Filter}}<div class="vazio"><strong>Nenhum prédio com esse filtro.</strong> <a href="/painel/grupos-implantacao">Ver todos</a></div>
{{else}}<div class="vazio"><strong>Nenhum prédio identificado no patrimônio</strong>Importe o <a href="/painel/configuracao?aba=patrimonio">patrimônio</a>: os prédios dele viram as unidades de implantação.</div>{{end}}
<div class="card deploy-sem-local"><div class="card-cab"><h2>Sem localização identificada</h2><span class="kpi-val">{{.Unlocated}}</span></div>
<p class="muted">Equipamentos sem bem do patrimônio (o tombo no nome). Não são postos em nenhum prédio nem no piloto e, por padrão, ficam fora das campanhas por localização; a campanha pode incluí-los explicitamente, com prazo ou horário fixo (não há janela de prédio para eles).</p></div>
</div>{{template "fim"}}`))

var _ = template.Must(pages.New("janelas-campos").Parse(`<fieldset class="deploy-dias"><legend>Dias da semana</legend>{{range .Days}}<label><input type="checkbox" name="dia" value="{{.Value}}" {{if .Checked}}checked{{end}}> {{.Label}}</label>{{end}}</fieldset>
<div class="location-fields deploy-horas"><label><span>Início</span><input type="time" name="inicio" value="{{.Start}}" required></label><label><span>Término</span><input type="time" name="fim" value="{{.End}}" required></label><label><span>Fuso horário</span><input name="fuso" value="{{.TZ}}" maxlength="64" required spellcheck="false"></label></div>`))

var _ = template.Must(pages.New("janelas-lote").Parse(`{{template "topo" .}}
<p><a href="/painel/grupos-implantacao">← Prédios e implantação</a></p>
<h1>Automação <span class="muted">› Janelas em lote</span></h1>
<div class="ux-locations ux-deploy">
<form class="busca deploy-filtro" method="get" action="/painel/grupos-implantacao/janelas">{{if .Sel}}<input type="hidden" name="sel" value="{{.Sel}}">{{end}}<input type="search" name="q" value="{{.Q}}" maxlength="100" placeholder="Pesquisar prédio" aria-label="pesquisar prédio">
<select name="janela" aria-label="janela de manutenção"><option value="">Todos os prédios</option><option value="com" {{if eq .Filter "com"}}selected{{end}}>Com janela ativa</option><option value="sem" {{if eq .Filter "sem"}}selected{{end}}>Sem janela ou desativada</option></select><button type="submit">Pesquisar</button></form>
{{if not .Admin}}<p class="aviso">Só o administrador altera as janelas; aqui fica a lista para consulta.</p>{{end}}
<form class="deploy-lote" method="post" action="/painel/grupos-implantacao/janelas">
<fieldset class="campo deploy-lista"><legend>Prédios</legend>
<p class="deploy-sel">Selecionar: <a href="{{.SelAll}}">todos ({{len .Buildings}})</a> · <a href="{{.SelNoWin}}">os sem janela</a> · <a href="{{.SelNone}}">limpar seleção</a></p>
{{if .Buildings}}<div class="deploy-checks">{{range .Buildings}}<label class="deploy-check"><input type="checkbox" name="predio" value="{{.ID}}" {{if .Checked}}checked{{end}}><span><b>{{localNome .Name}}</b><small>{{.Machines}} equip. · {{.WindowStateText}}{{if .HasWindow}}: {{.WindowText}}{{end}}</small></span></label>{{end}}</div>
<p class="deploy-contador muted"></p>{{else}}<p class="muted">Nenhum prédio com esse filtro.</p>{{end}}</fieldset>
<fieldset class="campo location-editor"><legend>Janela a aplicar</legend>{{template "janelas-campos" .}}</fieldset>
{{if .Admin}}<div class="acoes"><button type="submit" name="op" value="gravar">Aplicar aos marcados</button> <button class="perigo" type="submit" name="op" value="remover" formnovalidate>Remover a janela dos marcados</button>
<span class="muted">Antes de sobrescrever ou remover uma janela existente, o painel mostra quais serão afetadas e pede confirmação.</span></div>{{end}}</form>
</div>{{template "fim"}}`))

var _ = template.Must(pages.New("janelas-lote-confirmar").Parse(`{{template "topo" .}}
<p><a href="/painel/grupos-implantacao">← Prédios e implantação</a></p>
<h1>Confirmar janelas em lote</h1>
<div class="card"><dl class="dados"><dt>Prédios marcados</dt><dd>{{len .Chosen}}</dd><dt>Nova configuração</dt><dd>{{.New}}</dd><dt>Janelas existentes afetadas</dt><dd>{{len .Overwrite}}</dd></dl></div>
<div class="card"><div class="card-cab"><h2>{{if eq .Op "remover"}}Janelas que serão removidas{{else}}Janelas que serão sobrescritas{{end}}</h2></div><div class="tabela"><table><thead><tr><th>Prédio</th><th>Janela atual</th><th>Depois</th></tr></thead>
<tbody>{{range .Overwrite}}<tr><td>{{.Name}}</td><td>{{.Before}}</td><td>{{$.New}}</td></tr>{{end}}</tbody></table></div></div>
<form class="lote" method="post" action="/painel/grupos-implantacao/janelas"><input type="hidden" name="op" value="{{.Op}}"><input type="hidden" name="confirmar" value="1">
{{range .Chosen}}<input type="hidden" name="predio" value="{{.ID}}">{{end}}{{range .Days}}{{if .Checked}}<input type="hidden" name="dia" value="{{.Value}}">{{end}}{{end}}
<input type="hidden" name="inicio" value="{{.Start}}"><input type="hidden" name="fim" value="{{.End}}"><input type="hidden" name="fuso" value="{{.TZ}}">
<button type="submit" {{if eq .Op "remover"}}class="perigo"{{end}}>Confirmar e {{if eq .Op "remover"}}remover{{else}}aplicar{{end}} em {{len .Chosen}} prédio(s)</button> <a href="/painel/grupos-implantacao/janelas">Voltar sem mudar nada</a></form>
{{template "fim"}}`))

var _ = template.Must(pages.New("janelas-legadas").Parse(`{{template "topo" .}}
<p><a href="/painel/grupos-implantacao">← Prédios e implantação</a></p>
<h1>Automação <span class="muted">› Janelas dos grupos antigos</span></h1>
<p class="muted">Um prédio sem janela recebe a janela do grupo de implantação antigo só quando todas as máquinas dele (e o vínculo do prédio, se houver) estão em grupos com a mesma janela. Prédios que já têm janela nunca são alterados. Os grupos antigos continuam valendo para as campanhas antigas.</p>
<div class="card"><div class="card-cab"><h2>Sem ambiguidade</h2><span class="muted">{{len .Plan.Apply}} prédio(s)</span></div>
{{if .Plan.Apply}}<div class="tabela"><table><thead><tr><th>Prédio</th><th>Grupos das máquinas</th><th>Janela</th></tr></thead><tbody>{{range .Plan.Apply}}<tr><td>{{localNome .Name}}</td><td>{{.Groups}}</td><td>{{.WindowText}}</td></tr>{{end}}</tbody></table></div>
{{if .Admin}}<form class="lote" method="post" action="/painel/grupos-implantacao/janelas-legadas"><button type="submit">Aplicar as {{len .Plan.Apply}} janela(s)</button> <span class="muted">O servidor recalcula a lista na hora de aplicar.</span></form>{{end}}
{{else}}<p class="muted">Nenhum prédio sem janela com uma janela antiga inequívoca.</p>{{end}}</div>
<div class="card"><div class="card-cab"><h2>Pendentes de configuração</h2><span class="muted">{{len .Plan.Ambiguous}} prédio(s)</span></div>
{{if .Plan.Ambiguous}}<div class="tabela"><table><thead><tr><th>Prédio</th><th>Por que não é automático</th></tr></thead><tbody>{{range .Plan.Ambiguous}}<tr><td>{{localNome .Name}}</td><td>{{.Reason}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="muted">Nenhuma ambiguidade.</p>{{end}}</div>
{{template "fim"}}`))

// campanha-escopo: o escopo e o piloto do formulário de campanha (Campanhas e a página do
// programa). Os prédios começam recolhidos; o andar fica dentro do prédio.
var _ = template.Must(pages.New("campanha-escopo").Parse(`<fieldset class="campo escopo"><legend>Escopo da campanha</legend>
<p class="muted">{{.NBuildings}} prédio(s) com {{.NMachines}} equipamento(s){{if .NNoWindow}} · {{.NNoWindow}} sem janela ativa{{end}}{{if .NUnlocated}} · {{.NUnlocated}} sem localização identificada{{end}}. O prédio inteiro inclui também os equipamentos sem andar identificado; o andar, só os identificados nele. Prédio e andares dele juntos não repetem ninguém.</p>
<label class="opcao escopo-todos"><input type="checkbox" name="todos" value="1" {{if .All}}checked{{end}}><span><b>Todos os prédios identificados</b><small>os {{.NBuildings}} prédios com equipamentos</small></span></label>
{{if .Buildings}}<div class="escopo-lista">{{range .Buildings}}<details class="escopo-predio"{{if .Open}} open{{end}}><summary><label class="escopo-check"><input type="checkbox" name="predio" value="{{.ID}}" {{if .Checked}}checked{{end}} {{if not .Machines}}disabled{{end}}><b>{{localNome .Name}}</b></label>
<span class="escopo-info">{{.Machines}} equip.{{if .Floors}} · {{len .Floors}} andar(es){{end}}{{if .NoWindow}} · <span class="v-laranja">sem janela</span>{{end}}{{if not .Machines}} · sem equipamentos{{end}}</span></summary>
<div class="escopo-andares">{{range .Floors}}<label class="escopo-check"><input type="checkbox" name="andar" value="{{.Value}}" {{if .Checked}}checked{{end}}>{{.Label}} <span class="muted">· {{.Machines}}</span></label>{{else}}<span class="muted">Nenhum andar identificado (Configuração › Localizações).</span>{{end}}
{{if .Unfloored}}<span class="muted">{{.Unfloored}} sem andar: só com o prédio inteiro</span>{{end}}{{if .Conflicts}}<span class="v-laranja">{{.Conflicts}} com conflito de andar: só com o prédio inteiro</span>{{end}}<span class="muted">janela: {{if .WindowText}}{{.WindowText}}{{else}}nenhuma{{end}}</span></div></details>{{end}}</div>{{end}}
<p class="escopo-contador muted"></p>
{{if .Hidden}}<p class="muted">{{.Hidden}} prédio(s) fora da pesquisa "{{.Q}}" (entram com "Todos os prédios").</p>{{end}}
{{if .NUnlocated}}<label class="opcao"><input type="checkbox" name="sem_local" value="1" {{if .Unlocated}}checked{{end}}><span><b>Incluir os {{.NUnlocated}} equipamento(s) sem localização identificada</b><small>ficam fora por padrão; não pertencem a nenhum prédio, não vão para o piloto e não têm janela de prédio (só com prazo ou horário fixo)</small></span></label>
<label class="opcao"><input type="checkbox" name="sem_local_ok" value="1" {{if .UnlocatedOK}}checked{{end}}><span><b>Confirmo a inclusão dos sem localização</b></span></label>{{end}}
</fieldset>
<fieldset class="campo piloto"><legend>Implantação piloto</legend>
<label class="opcao"><input type="radio" name="piloto" value="" {{if eq .Pilot ""}}checked{{end}}><span><b>Sem piloto</b><small>todos os equipamentos do escopo recebem na primeira liberação, cada um no horário que vale para ele</small></span></label>
<label class="opcao"><input type="radio" name="piloto" value="predio" {{if eq .Pilot "predio"}}checked{{end}}><span><b>Um prédio inteiro como piloto</b>
<select name="piloto_predio" aria-label="prédio piloto"><option value="">escolha o prédio</option>{{range .PilotBuildings}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</select></span></label>
<label class="opcao"><input type="radio" name="piloto" value="andar" {{if eq .Pilot "andar"}}checked{{end}}><span><b>Um andar como piloto</b>
<select name="piloto_andar" aria-label="andar piloto"><option value="">escolha o andar</option>{{range .PilotFloors}}<optgroup label="{{.Label}}">{{range .Options}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}</select></span></label>
<small class="muted">O piloto precisa estar no escopo e ter equipamentos; no piloto por andar, só os identificados naquele andar (nunca os em conflito). Depois de o piloto terminar sem falha e da observação, o resto do escopo é liberado de uma vez, cada prédio na sua janela; o piloto não recebe de novo. Falha ou piloto sem nenhum job concluído pausam a campanha.</small></fieldset>`))

// campanha-escopo-conferir: o escopo resolvido pelo servidor, na conferência.
var _ = template.Must(pages.New("campanha-escopo-conferir").Parse(`<div class="card"><div class="card-cab"><h2>Etapas</h2><span class="muted">{{.Total}} equipamento(s) no total</span></div><div class="tabela"><table><thead><tr><th>Ordem</th><th>Etapa</th><th>Equipamentos</th><th>Prédios</th>{{if (index .Stages 0).Preview}}<th>Hoje</th>{{end}}</tr></thead>
<tbody>{{range $i, $s := .Stages}}<tr><td class="num">{{$i}}</td><td>{{$s.Label}}{{if $s.Pilot}} <span class="estado em_dia">piloto</span>{{end}}</td><td class="num">{{$s.Machines}}</td><td class="num">{{$s.Buildings}}</td>{{if (index $.Stages 0).Preview}}<td>{{$s.Preview}}</td>{{end}}</tr>{{end}}</tbody></table></div></div>
<div class="card"><div class="card-cab"><h2>Prédios do escopo</h2><span class="muted">{{len .Buildings}} prédio(s){{if .UnlocatedN}} · {{.UnlocatedN}} sem localização{{end}}</span></div><div class="tabela"><table><thead><tr><th>Prédio</th><th>Equipamentos</th><th>No piloto</th><th>Depois</th><th>Janela</th><th>Próxima</th></tr></thead>
<tbody>{{range .Buildings}}<tr><td>{{.Name}}</td><td class="num">{{.Total}}</td><td class="num">{{.Pilot}}</td><td class="num">{{.Rest}}</td><td>{{if .NoWindow}}<span class="v-laranja">{{.Window}}</span>{{else}}{{.Window}}{{end}}</td><td>{{.Next}}</td></tr>{{end}}</tbody></table></div>
{{if .Floors}}<p class="muted">Andares escolhidos: {{range $i, $f := .Floors}}{{if $i}}; {{end}}{{$f}}{{end}}.</p>{{end}}
{{if .Excluded}}<p class="muted">Fora do escopo: {{range $i, $e := .Excluded}}{{if $i}}; {{end}}{{$e}}{{end}}.</p>{{end}}</div>`))
