package api

import (
	"net/http"
	"strings"

	"github.com/tasantiago/patchd/internal/texto"
)

// Ajuda local e orientada a tarefas. Não consulta máquinas, não executa ações e não
// depende de acesso à internet. Os links administrativos seguem o perfil da sessão.
type guideLink struct{ Label, Href string }
type panelGuide struct {
	ID, Title, Summary, Keywords string
	Steps                        []string
	Links                        []guideLink
	Admin                        bool
}

var panelGuides = []panelGuide{
	{ID: "computadores", Title: "Encontrar um computador e entender sua situação", Summary: "Veja o que precisa de atenção e consulte os dados do equipamento.", Keywords: "parque frota inventario aposentadas buscar maquina offline check-in",
		Steps: []string{"Use a busca do topo por nome, IP, usuário, série ou patrimônio para abrir o computador.", "Em Situação do parque, filtre os equipamentos com atualizações pendentes ou sem contato. Em Inventário, consulte hardware e programas instalados.", "Sem contato não significa necessariamente desligado. Confira a data da última comunicação e as evidências antes de agir. Aposentadas reúne equipamentos retirados do parque ativo."},
		Links: []guideLink{{"Ver situação do parque", "/painel/parque"}, {"Consultar inventário", "/painel/inventario"}, {"Ver aposentadas", "/painel/aposentadas"}}},
	{ID: "atualizacoes", Title: "Aprovar e instalar atualizações", Summary: "Separe a escolha do que pode instalar da execução nos computadores.", Keywords: "patch patches kb windows linux terceiros programas aprovar instalar",
		Steps: []string{"Comece pela visão geral de Atualizações para identificar prioridades e falhas nas buscas.", "Nas abas Sistema operacional e Programas de terceiros, um administrador confere e aprova as versões. Aprovar permite instalar; não inicia uma instalação sozinho.", "Para distribuir a atualização, crie uma campanha e confira os destinos e o horário. Acompanhe o resultado em Execuções; um reinício, quando necessário, é uma ação separada."},
		Links: []guideLink{{"Analisar atualizações", "/painel/patch?aba=geral"}, {"Acompanhar campanhas", "/painel/campanhas"}}},
	{ID: "campanhas", Title: "Distribuir uma atualização com uma campanha", Summary: "Escolha o que fazer, onde executar e em qual horário.", Keywords: "piloto janela horario predio andar implantar distribuir reiniciar",
		Steps: []string{"Escolha o tipo de tarefa e os prédios ou andares que devem receber a atualização. Use um piloto antes de ampliar a distribuição.", "Defina quando executar e revise a conferência. A campanha só é criada depois da confirmação final.", "Se a campanha pausar, leia o motivo. Reenviar tenta novamente o que ainda precisa; Retomar aceita as falhas da etapa e permite seguir. Uma execução concluída ainda precisa da evidência posterior do computador para confirmar conformidade."},
		Links: []guideLink{{"Ver campanhas", "/painel/campanhas"}, {"Conferir prédios e janelas", "/painel/grupos-implantacao"}}},
	{ID: "execucoes", Title: "Acompanhar execuções e investigar uma falha", Summary: "Uma execução, também chamada job, é uma tarefa enviada ao agente.", Keywords: "jobs job erro falhou recusado pendente expirado adiado",
		Steps: []string{"Na lista de execuções, confira o computador, o tipo da tarefa, o prazo e o resultado. Abra o computador para ver suas evidências.", "Pendente significa que ainda aguarda entrega ou horário. Adiado pode indicar um programa aberto ou outra instalação em andamento. Expirado significa que o prazo terminou.", "Em caso de falha ou recusa, leia o motivo antes de tentar novamente. Confira a comunicação e a versão do agente; repetir não corrige falta de espaço, assinatura inválida ou bloqueio de política."},
		Links: []guideLink{{"Ver execuções", "/painel/jobs"}}},
	{ID: "pedidos", Title: "Atender pedidos da Central de software", Summary: "Acompanhe o que os usuários solicitaram e as decisões da TI.", Keywords: "autoatendimento loja aplicativos solicitacao aprovacao central",
		Steps: []string{"Abra os pedidos de software e confira aplicativo, computador, usuário e situação.", "Os pedidos que exigem aprovação aguardam a decisão de um administrador. Aprovar refaz a conferência e cria a tarefa; recusar exige um motivo.", "Acompanhe a execução após a aprovação. A oferta de aplicativos da Central é configurada separadamente do histórico de pedidos."},
		Links: []guideLink{{"Ver pedidos de software", "/painel/autoatendimento"}}},
	{ID: "catalogo", Title: "Escolher o que oferecer na Central de software", Summary: "Defina os aplicativos que os usuários podem instalar ou atualizar.", Keywords: "autoatendimento oferta loja aplicativos catalogo grupos aprovacao", Admin: true,
		Steps: []string{"Prepare e aprove a versão do programa antes de oferecê-lo na Central.", "No catálogo da Central, escolha se o usuário pode instalar do zero, atualizar ou pedir aprovação da TI. Confira os grupos de usuários autorizados.", "Salve as alterações e use a prévia da loja para validar a oferta em um computador. Os pedidos enviados pelos usuários são acompanhados em Pedidos de software."},
		Links: []guideLink{{"Configurar catálogo da Central", "/painel/configuracao?aba=autoatendimento"}, {"Ver pedidos de software", "/painel/autoatendimento"}}},
	{ID: "protecao", Title: "Entender os indicadores de proteção", Summary: "O Shield reúne critérios de proteção e a evolução dos locais.", Keywords: "shield selo pontuacao ranking protecao seguranca",
		Steps: []string{"Confira a situação e os critérios que impedem o selo de proteção.", "Use os filtros por prédio e andar para investigar as pendências. Um dado desconhecido não deve ser interpretado como proteção confirmada.", "O percentual de computadores no selo e a posição no ranking são medidas diferentes. A pontuação depende das evidências recebidas, não apenas do sucesso de uma tarefa."},
		Links: []guideLink{{"Ver proteção do ambiente", "/painel/shield"}}},
	{ID: "alertas", Title: "Tratar um alerta de desempenho", Summary: "Investigue CPU, memória e disco antes de encerrar o alerta.", Keywords: "desempenho cpu memoria disco limpar reconhecer fechar",
		Steps: []string{"Abra o alerta e consulte o período, o computador e os gráficos de desempenho.", "Reconhecer registra que a equipe tomou ciência; não resolve nem encerra o problema.", "Um administrador pode fechar manualmente ou solicitar a limpeza dos itens permitidos em um alerta de disco. Confira os itens; um problema persistente pode gerar outro alerta."},
		Links: []guideLink{{"Ver alertas", "/painel/alertas"}}},
	{ID: "implantacao", Title: "Organizar prédios e horários de implantação", Summary: "Defina onde e quando as campanhas podem executar.", Keywords: "grupos aneis rings predios andares janela implantacao",
		Steps: []string{"Confira quais computadores pertencem a cada prédio e andar antes de criar a campanha.", "Um administrador configura as janelas de manutenção. Use o fuso exibido na tela ao escolher o horário.", "Prédios e janelas organizam a implantação. Os padrões de aplicativos e atalhos são configurados em Padronização."},
		Links: []guideLink{{"Ver prédios e implantação", "/painel/grupos-implantacao"}}},
	{ID: "agentes", Title: "Adicionar computadores ao servidor", Summary: "Prepare um agente, registre um piloto e confira o primeiro inventário.", Keywords: "agente instalar token registro enrollment checkin rele", Admin: true,
		Steps: []string{"Em Configuração › Agentes, confira a versão publicada, o executável correto e o certificado necessário.", "Crie um token de registro com validade e quantidade de usos adequadas. Use o comando de instalação gerado; para registro pelo relé, use o token específico desse fluxo.", "Depois da instalação, procure o computador no Inventário e aguarde os dados. Nunca compartilhe tokens, credenciais ou chaves em chamados públicos."},
		Links: []guideLink{{"Preparar agentes", "/painel/configuracao?aba=agente"}}},
	{ID: "padronizacao", Title: "Definir aplicativos e atalhos por setor", Summary: "Crie um padrão básico e complemente com o que cada setor precisa.", Keywords: "financeiro rh padrao aplicativos icones atalhos instalar grupos", Admin: true,
		Steps: []string{"Prepare os aplicativos e os scripts necessários, com as regras de detecção de instalação.", "Crie o padrão básico e os padrões dos setores. Confira as associações por computador e por grupo do usuário; vários padrões podem se somar.", "Teste em poucos computadores antes de ativar para todos. O agente aplica o padrão conforme as capacidades de cada sistema."},
		Links: []guideLink{{"Configurar padronização", "/painel/padronizacao"}, {"Preparar programas", "/painel/patch?aba=terceiros"}}},
	{ID: "configuracao", Title: "Configurar o ambiente", Summary: "Escolha a tarefa; os recursos técnicos ficam em Mais configurações.", Keywords: "sistema preferencias parametros acesso usuarios administracao", Admin: true,
		Steps: []string{"Use os cartões da página Geral para localizar a configuração desejada.", "Em Preferências, cada cartão salva apenas suas próprias alterações. As mudanças de acesso, catálogo da Central e instalação em rede ficam nas áreas específicas.", "Consulte Histórico de alterações para conferir quem mudou uma configuração. Dados de diagnóstico do servidor ficam recolhidos na página Geral."},
		Links: []guideLink{{"Abrir configuração", "/painel/configuracao"}}},
	{ID: "localizacoes", Title: "Identificar prédios, andares e salas", Summary: "Relacione as salas aos andares usados nas telas e campanhas.", Keywords: "localizacao localizacoes regex expressao regular faixas sala patrimonio", Admin: true,
		Steps: []string{"Abra o prédio e confira como as salas são identificadas no inventário ou patrimônio.", "Cadastre a faixa do andar com a regra que corresponde às salas. Confira os exemplos e os conflitos antes de salvar.", "Depois de salvar, valide alguns computadores. Essa configuração de localização não instala programas nem executa campanhas."},
		Links: []guideLink{{"Configurar localizações", "/painel/configuracao?aba=localizacoes"}}},
	{ID: "acesso", Title: "Administrar usuários e permissões", Summary: "Diferencie quem consulta de quem pode alterar o ambiente.", Keywords: "login ad oidc rhbk senha usuarios acesso permissao", Admin: true,
		Steps: []string{"Confira o perfil necessário: Leitura consulta; Administrador também aprova e executa as ações permitidas.", "Use Usuários e acesso para configurar os provedores e as contas locais de contingência.", "Teste o acesso antes de remover o método anterior. Consulte o histórico quando houver uma alteração de permissão."},
		Links: []guideLink{{"Ver usuários e acesso", "/painel/configuracao?aba=acesso"}, {"Consultar histórico", "/painel/configuracao?aba=auditoria"}}},
	{ID: "preferencias", Title: "Ajustar preferências sem reiniciar", Summary: "Altere os valores disponíveis na tela e confira o efeito descrito no campo.", Keywords: "parametros ambiente env fuso intervalo checkin notificacoes preferencias", Admin: true,
		Steps: []string{"Encontre o cartão do assunto e leia a ajuda ao lado do campo.", "Clique em Salvar no cartão alterado. Um erro de validação impede a gravação; corrija os campos marcados.", "Origem e restauração mostra o valor de referência e permite restaurá-lo. Portas, certificados e segredos continuam na configuração de implantação do servidor."},
		Links: []guideLink{{"Ajustar preferências", "/painel/configuracao?aba=parametros"}}},
	{ID: "ferramentas", Title: "Usar scripts e consultas técnicas", Summary: "Recursos avançados ficam separados da rotina de atualização.", Keywords: "scripts zip pacote run parametros perguntas consultas automacao", Admin: true,
		Steps: []string{"Perguntas ao parque consulta informações de leitura; ausência de resposta não comprova ausência do item.", "Em Scripts, confira o pacote, o arquivo de entrada e os parâmetros antes de escolher os destinos.", "Uma consulta não instala aplicativos. Um script pode alterar o computador: revise o conteúdo e teste em um piloto antes de ampliar."},
		Links: []guideLink{{"Consultar o parque", "/painel/perguntas"}, {"Gerenciar scripts", "/painel/scripts"}}},
	{ID: "saude", Title: "Investigar lentidão ou falta de espaço no servidor", Summary: "Leia o diagnóstico e as recomendações antes de alterar recursos.", Keywords: "saude cpu memoria disco banco postgres conexoes desempenho", Admin: true,
		Steps: []string{"Comece pelo diagnóstico e pelas recomendações da página Saúde do servidor.", "Compare os gráficos no período do problema. Reiniciar o processo zera as amostras recentes em memória; os agregados já gravados continuam no histórico.", "Use as seções técnicas para investigar consultas, conexões e rotas lentas. Confira se a falta de espaço está no volume do banco ou em outra pasta antes de agir."},
		Links: []guideLink{{"Ver saúde do servidor", "/painel/configuracao?aba=saude"}}},
	{ID: "historico", Title: "Conferir quem alterou o ambiente", Summary: "Consulte o histórico para investigar decisões e mudanças.", Keywords: "auditoria historico alteracoes usuario data motivo csv", Admin: true,
		Steps: []string{"Escolha os filtros do histórico e localize o período da alteração.", "Confira a ação, o usuário, o objeto e o motivo registrados. Uma ação administrativa e o resultado posterior no computador são evidências diferentes.", "Use a exportação CSV quando precisar compartilhar os registros com a equipe. Não inclua credenciais ou chaves em chamados."},
		Links: []guideLink{{"Abrir histórico de alterações", "/painel/configuracao?aba=auditoria"}}},
	{ID: "descoberta", Title: "Descobrir computadores e instalar pela rede", Summary: "Prepare o acesso às estações antes de iniciar uma varredura.", Keywords: "descoberta winrm instalacao rede faixas credencial windows", Admin: true,
		Steps: []string{"Confira os requisitos de rede, certificado e conta de instalação na configuração de Instalação em rede.", "Na descoberta, cadastre as faixas autorizadas e execute a varredura. Ausência de resposta pode ser firewall ou falta de rota, não apenas computador desligado.", "Revise os resultados antes de pedir a instalação. Acompanhe o resultado de cada estação e confirme seu primeiro inventário."},
		Links: []guideLink{{"Preparar instalação em rede", "/painel/configuracao?aba=descoberta"}, {"Ver descoberta de rede", "/painel/descoberta"}}},
	{ID: "patrimonio", Title: "Integrar os dados de patrimônio", Summary: "Relacione os equipamentos aos dados de prédio, sala e patrimônio.", Keywords: "patrimonio importar csv integracao tombamento localizacao", Admin: true,
		Steps: []string{"Leia o formato e os campos aceitos na página de integração com patrimônio.", "Confira os identificadores dos equipamentos antes de importar e leia o resultado apresentado.", "Valide alguns computadores no Inventário. A localização importada e as regras de andares são configurações relacionadas, mas diferentes."},
		Links: []guideLink{{"Ver integração com patrimônio", "/painel/configuracao?aba=patrimonio"}}},
	{ID: "bios", Title: "Administrar as senhas da BIOS", Summary: "A senha padrão por fabricante e as operações em uma máquina têm escopos diferentes.", Keywords: "bios senha fabricante firmware guarda chave", Admin: true,
		Steps: []string{"Confira os fabricantes e a disponibilidade da chave de guarda na configuração de senhas da BIOS.", "Cadastrar uma senha padrão no servidor não comprova que ela já foi aplicada a cada computador.", "Para uma operação em uma máquina, abra seu detalhe, confira o suporte e leia a confirmação. Acompanhe o resultado antes de considerar a alteração concluída."},
		Links: []guideLink{{"Ver senhas da BIOS", "/painel/configuracao?aba=bios"}}},
}

func (b pageBase) PageGuide() *panelGuide {
	id := map[string]string{"inicio": "computadores", "frota": "computadores", "inventario": "computadores", "aposentadas": "computadores", "patch": "atualizacoes", "campanhas": "campanhas", "jobs": "execucoes", "autoatendimento": "pedidos", "shield": "protecao", "alertas": "alertas", "aneis": "implantacao", "padronizacao": "padronizacao", "scripts": "ferramentas", "descoberta": "descoberta", "configuracao": "configuracao"}[b.Nav]
	if b.Nav == "campanhas" && b.Sub == "perguntas" {
		id = "ferramentas"
	}
	if b.Nav == "configuracao" {
		if selected := map[string]string{"agente": "agentes", "parametros": "preferencias", "localizacoes": "localizacoes", "acesso": "acesso", "autoatendimento": "catalogo", "saude": "saude", "auditoria": "historico", "descoberta": "descoberta", "patrimonio": "patrimonio", "bios": "bios"}[b.Sub]; selected != "" {
			id = selected
		}
	}
	for i := range panelGuides {
		if g := &panelGuides[i]; g.ID == id && (!g.Admin || b.Admin) {
			return g
		}
	}
	return nil
}

type helpView struct {
	pageBase
	Query string
	Tasks []panelGuide
}

func (a *api) helpPage(w http.ResponseWriter, r *http.Request) {
	v := helpView{pageBase: a.base(r, "Ajuda e primeiros passos", "ajuda"), Query: texto.Clip(strings.TrimSpace(r.URL.Query().Get("q")), 100)}
	words := strings.Fields(guideSearch(v.Query))
	for _, task := range panelGuides {
		if task.Admin && !v.Admin || task.ID == "preferencias" && !v.ParamsOn || task.ID == "saude" && a.health == nil {
			continue
		}
		text := guideSearch(task.Title + " " + task.Summary + " " + task.Keywords + " " + strings.Join(task.Steps, " "))
		match := true
		for _, word := range words {
			if !strings.Contains(text, word) {
				match = false
				break
			}
		}
		if match {
			v.Tasks = append(v.Tasks, task)
		}
	}
	a.render(w, r, http.StatusOK, "ajuda", v)
}

func guideSearch(s string) string {
	return strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ü", "u", "ç", "c").Replace(strings.ToLower(s))
}

type settingsNavItem struct {
	Label, Href string
	Active      bool
}

func (v settingsView) SettingsPrimary() []settingsNavItem {
	return v.settingsItems([][2]string{{"sistema", "Geral"}, {"agente", "Agentes"}, {"parametros", "Preferências"}, {"localizacoes", "Prédios e salas"}, {"acesso", "Usuários e acesso"}})
}

func (v settingsView) SettingsMore() []settingsNavItem {
	return v.settingsItems([][2]string{{"autoatendimento", "Catálogo da Central"}, {"saude", "Saúde do servidor"}, {"auditoria", "Histórico de alterações"}, {"patrimonio", "Integração com patrimônio"}, {"descoberta", "Instalação em rede"}, {"bios", "Senhas da BIOS"}})
}

func (v settingsView) settingsItems(items [][2]string) []settingsNavItem {
	var out []settingsNavItem
	for _, item := range items {
		if item[0] == "parametros" && !v.ParamsOn || item[0] == "saude" && !v.HealthOn {
			continue
		}
		href := "/painel/configuracao"
		if item[0] != "sistema" {
			href += "?aba=" + item[0]
		}
		out = append(out, settingsNavItem{Label: item[1], Href: href, Active: v.Tab == item[0]})
	}
	return out
}

func (v settingsView) SettingsMoreOpen() bool {
	for _, item := range v.SettingsMore() {
		if item.Active {
			return true
		}
	}
	return false
}

func (v settingsView) DiagnosticsAttention() bool {
	for _, row := range v.System {
		if row.Level == "erro" {
			return true
		}
	}
	return false
}
