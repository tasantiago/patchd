package api

import (
	"context"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Trilha de auditoria (Aula 15.13). Cada ação que muda alguma coisa pelo painel, cada login
// (aceito ou recusado) e cada exportação vira um evento no banco, ao lado da linha de log de
// sempre: o log é para operar (e gira); a trilha é para responder, meses depois, "quem
// aprovou isso?" ou "quem exportou a lista de máquinas?".
//
// A gravação é melhor esforço: se o banco falhar, a ação já aconteceu e não se desfaz; o
// erro vai para o log como ERROR ("auditoria não gravada"), para a monitoração pegar.

// auditTimeout: a gravação não espera o navegador (o contexto da requisição pode ter sido
// cancelado depois da ação), mas também não segura a resposta.
const auditTimeout = 5 * time.Second

// audit registra a ação de quem está na sessão.
func (a *api) audit(r *http.Request, s panel.Session, action, target, detail string) {
	a.auditAs(r, s.Username, s.Source, action, target, detail)
}

// auditAs registra uma ação sem sessão (login recusado, por exemplo).
func (a *api) auditAs(r *http.Request, user, source, action, target, detail string) {
	e := store.AuditEvent{At: a.now(), Username: user, Source: source, Action: action, Target: target, Detail: detail,
		Remote: remoteIP(r), RequestID: RequestID(r.Context())}
	if e.Username == "" {
		e.Username = "(desconhecido)"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := a.store.RecordAudit(ctx, e); err != nil {
		a.logger.Error("auditoria não gravada", "action", action, "error", err, "request_id", e.RequestID)
	}
}

// auditActions: as ações da trilha, como aparecem no painel (Aula 15.14) e na linha de
// comando. A chave é o que fica no banco; mudar o texto não muda a trilha.
var auditActions = []struct{ Key, Label string }{
	{"login.ok", "Login"},
	{"login.recusado", "Login recusado"},
	{"login.bloqueado", "Login bloqueado (tentativas)"},
	{"sessao.sair", "Saída"},
	{"sessao.encerrar", "Sessão encerrada"},
	{"sessao.encerrar_todas", "Sessões de um usuário encerradas"},
	{"ad.testar", "Teste de login no AD"},
	{"oidc.testar", "Teste do RHBK"},
	{"usuario.criar", "Usuário local criado"},
	{"usuario.senha", "Senha de usuário local trocada"},
	{"usuario.remover", "Usuário local removido"},
	{"token.criar", "Token de registro criado"},
	{"token.revogar", "Token de registro revogado"},
	{"agente.baixar", "Agente baixado"},
	{"patch.aprovar", "Item aprovado"},
	{"patch.aprovar_lote", "Lote aprovado"},
	{"patch.aprovar_correcao", "Correção aprovada"},
	{"patch.revogar", "Aprovação revogada"},
	{"terceiros.aprovar", "Instalador aprovado"},
	{"terceiros.revogar", "Aprovação de instalador revogada"},
	{"terceiros.baixar", "Download de instalador pedido"},
	{"terceiros.excluir", "Versão de instalador excluída"},
	{"patrimonio.importar", "Patrimônio importado"},
	{"descoberta.faixa", "Faixa de descoberta alterada"},
	{"descoberta.varrer", "Varredura de rede iniciada"},
	{"job.criar", "Job criado"},
	{"job.cancelar", "Job cancelado"},
	{"autoatendimento.oferta", "Oferta do autoatendimento gravada"},
	{"autoatendimento.remover", "Programa fora do autoatendimento"},
	{"autoatendimento.oferta_lote", "Ofertas do autoatendimento gravadas em lote"},
	{"autoatendimento.pedido", "Pedido do autoatendimento atendido"},
	{"autoatendimento.recusado", "Pedido do autoatendimento recusado"},
	{"autoatendimento.solicitacao", "Pedido do autoatendimento aguardando a TI"},
	{"autoatendimento.aprovar", "Pedido do autoatendimento aprovado pela TI"},
	{"autoatendimento.reprovar", "Pedido do autoatendimento recusado pela TI"},
	{"campanha.criar", "Campanha criada"},
	{"campanha.alterar", "Campanha alterada"},
	{"janela.definir", "Janela definida"},
	{"janela.remover", "Janela removida"},
	{"maquina.anel", "Máquina mudou de anel"},
	{"maquina.anel_lote", "Máquinas mudaram de anel"},
	{"maquina.aposentar", "Máquina aposentada"},
	{"maquina.restaurar", "Máquina restaurada"},
	{"bios.senha.preparar", "Senha da BIOS sorteada e guardada"},
	{"bios.senha.revelar", "Senha da BIOS revelada"},
	{"bios.senha.gravar", "Gravação da senha da BIOS pedida"},
	{"bios.senha.resolver", "Senha da BIOS incerta resolvida"},
	{"exportar.csv", "Exportação CSV"},
	{"acesso.negado", "Acesso negado (perfil)"},
	// Aula 18.1: a linha de comando (patchd-server no terminal) e a retenção da trilha.
	{"cli.job.keygen", "Chave de jobs gerada (terminal)"},
	{"cli.bios.keygen", "Chave da guarda da BIOS gerada (terminal)"},
	{"cli.job.create", "Job criado (terminal)"},
	{"cli.job.cancel", "Job cancelado (terminal)"},
	{"cli.campaign.create", "Campanha criada (terminal)"},
	{"cli.campaign.pause", "Campanha pausada (terminal)"},
	{"cli.campaign.resume", "Campanha retomada (terminal)"},
	{"cli.campaign.cancel", "Campanha cancelada (terminal)"},
	{"cli.ring.set", "Máquinas mudaram de anel (terminal)"},
	{"cli.ring.window", "Janela definida (terminal)"},
	{"cli.machine.retire", "Máquina aposentada (terminal)"},
	{"cli.machine.restore", "Máquina restaurada (terminal)"},
	{"cli.token.create", "Token de registro criado (terminal)"},
	{"cli.token.revoke", "Token de registro revogado (terminal)"},
	{"cli.user.create", "Usuário local criado (terminal)"},
	{"cli.user.password", "Senha de usuário local trocada (terminal)"},
	{"cli.user.delete", "Usuário local removido (terminal)"},
	{"cli.package.fetch", "Instalador baixado (terminal)"},
	{"cli.package.install", "Job de programa criado (terminal)"},
	{"cli.package.approve", "Instalador aprovado (terminal)"},
	{"cli.package.revoke", "Aprovação de instalador revogada (terminal)"},
	{"cli.release.publish", "Versão do agente publicada (terminal)"},
	{"cli.release.withdraw", "Oferta de versão do agente retirada (terminal)"},
	{"cli.retention.apply", "Retenção aplicada (terminal)"},
	{"cli.repo-cache.prune", "Cache de repositórios limpo (terminal)"},
	{"auditoria.retencao", "Eventos antigos da auditoria apagados"},
	// Aula 18.5: notificações por e-mail.
	{"notificacao.teste", "Notificação de teste enviada"},
	{"cli.notify.test", "Notificação de teste enviada (terminal)"},
	// Aula 19.1: aparência do painel.
	{"aparencia.logo", "Logotipo do painel trocado"},
	{"aparencia.logo_remover", "Logotipo do painel removido"},
	// Aula 19.2: parâmetros pela tela.
	{"parametro.alterar", "Parâmetro alterado pela tela"},
	{"parametro.restaurar", "Parâmetro restaurado para o .env"},
	{"parametro.sessoes", "Sessões encerradas: grupo de acesso tirado da lista"},
	{"cli.settings.reset", "Parâmetro restaurado para o .env (terminal)"},
	{"cli.session.create", "Sessão de varredura aberta (terminal)"}, // Aula 20.4
	{"cli.session.end", "Sessões de varredura encerradas (terminal)"},
}

// AuditLabel: o rótulo da ação; a chave quando não há (ação de uma versão mais nova).
func AuditLabel(key string) string {
	for _, x := range auditActions {
		if x.Key == key {
			return x.Label
		}
	}
	return key
}
