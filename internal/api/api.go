// Package api implementa a API REST do patchd-server.
//
// Rotas do agente (Aula 4.3): o registro (/api/v1/enroll) exige um token de enrollment;
// os envios (/api/v1/agent/...) exigem a credencial da máquina, e a máquina é a dona da
// credencial, nunca um ID vindo da URL.
//
// Painel (Aula 7.2): /painel/entrar abre uma sessão (cookie patchd_sessao) e as rotas de
// leitura (/api/v1/machines..., /api/v1/identity-links) exigem a sessão. Sem TLS (Aula
// 9.1), a senha e o cookie trafegam em texto claro: a porta fica restrita a 127.0.0.1.
package api

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/window"
)

// Store é o armazenamento de que a API precisa. Definido aqui, onde é usado: qualquer
// tipo com estes métodos serve (a memória, o PostgreSQL, um fake de teste).
type Store interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (stored bool, err error)
	LatestInventory(ctx context.Context, machineID string) (protocol.InventoryReport, bool, error)
	SaveScan(ctx context.Context, machineID string, rep protocol.PatchScanReport) error
	LatestScan(ctx context.Context, machineID string) (protocol.PatchScanReport, bool, error)
	// Autoatendimento (Aula 17.1).
	SelfServiceOffers(ctx context.Context) ([]store.SelfServiceOffer, error)
	SetSelfServiceOffer(ctx context.Context, o store.SelfServiceOffer) error
	DeleteSelfServiceOffer(ctx context.Context, app string) (bool, error)
	// Pedidos que esperam a TI (Aula 18.3).
	CreateSelfServiceRequest(ctx context.Context, q store.SelfServiceRequest) (int64, error)
	SelfServiceRequests(ctx context.Context, f store.RequestFilter) ([]store.SelfServiceRequest, error)
	SelfServiceRequestByID(ctx context.Context, id int64) (store.SelfServiceRequest, bool, error)
	DecideSelfServiceRequest(ctx context.Context, id int64, state, by, note string, jobID int64, at time.Time) error
	ExpireSelfServiceRequests(ctx context.Context, before, now time.Time) (int64, error)
	// KeyStats: as chaves de jobs e de release aceitas pela frota (Aula 20.7).
	KeyStats(ctx context.Context, since time.Time) (store.KeyStats, error)
	// SourceFailures: as fontes que falharam na busca atual (Aula 16.2); machineID vazio = todas.
	SourceFailures(ctx context.Context, machineID string) ([]store.SourceFailure, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	// Enroll consome um uso do token e registra a máquina; token inválido devolve
	// um erro que satisfaz errors.Is(err, identity.ErrEnrollmentRejected).
	// Também cria os alertas de identidade contra as máquinas já conhecidas e os devolve.
	Enroll(ctx context.Context, tokenHash []byte, m identity.NewMachine) ([]protocol.IdentityLink, error)
	MachineByCredential(ctx context.Context, credentialHash []byte) (machineID string, found bool, err error)
	IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error)
	// CheckIn registra o contato periódico (e o canal: TLS ou não, Aula 9.2) e diz se o
	// inventário atual tem o hash informado.
	CheckIn(ctx context.Context, machineID, agentVersion, inventoryHash string, viaTLS bool) (inventoryKnown bool, err error)
	// CheckinTLS diz por qual canal chegou o último check-in (nil: nenhum desde a Aula 9.2).
	CheckinTLS(ctx context.Context, machineID string) (*bool, error)
	// Rede e discos do último check-in, e o endereço de onde ele veio (Aula 12.1).
	SaveHostState(ctx context.Context, machineID string, state *protocol.MachineState, addr string) error
	HostState(ctx context.Context, machineID string) (store.HostState, bool, error)
	// Lista de Equipamentos (Aula 12.2).
	InventoryList(ctx context.Context) ([]store.InventoryRow, error)
	// Aprovação de patches (Aula 13.1).
	PatchApprovals(ctx context.Context) ([]store.PatchApproval, error)
	ApprovePatch(ctx context.Context, a store.PatchApproval) (bool, error)
	ApprovePatches(ctx context.Context, list []store.PatchApproval) ([]store.PatchApproval, error)
	RevokePatch(ctx context.Context, item, fixedIn string) (bool, error)
	// Instaladores de terceiros guardados e a aprovação deles (Aulas 14.2 e 14.5).
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	ThirdPartyApprovals(ctx context.Context) ([]store.ThirdPartyApproval, error)
	ApproveThirdParty(ctx context.Context, a store.ThirdPartyApproval) (bool, error)
	RevokeThirdParty(ctx context.Context, app, version string) (bool, error)
	// Usuários e sessões do painel.
	PanelStore
	// Máquinas aposentadas (Aula 7.5): o painel lista, aposenta e restaura (Aula 7.6).
	RetiredMachines(ctx context.Context) ([]protocol.RetiredMachine, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	RestoreMachine(ctx context.Context, id string) (bool, error)
	// Fila de jobs (Aula 8.1).
	JobStore
	// Anel e janela da máquina, para o detalhe (Aula 8.3).
	MachineRing(ctx context.Context, id string) (int, bool, error)
	Windows(ctx context.Context) ([]store.RingWindow, error)
	// Anéis e janelas pelo painel (Aula 11.1).
	SetMachineRing(ctx context.Context, id string, ring int) (bool, error)
	SetWindow(ctx context.Context, ring int, w window.Window, by string) error
	DeleteWindow(ctx context.Context, ring int) (bool, error)
	// Campanhas, para a tela (Aula 8.5) e para criar e controlar pelo painel (Aula 11.2).
	Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error)
	CreateCampaign(ctx context.Context, c campaign.Campaign) (int64, error)
	Campaign(ctx context.Context, id int64) (campaign.Campaign, bool, error)
	SaveCampaign(ctx context.Context, c campaign.Campaign, from string) (bool, error)
	CampaignJobs(ctx context.Context, id int64, idx int) ([]campaign.JobState, error)
	// Tokens de registro pelo painel (Aula 15.2): os mesmos do patchd-server token.
	CreateEnrollmentToken(ctx context.Context, hash []byte, note string, expiresAt time.Time, maxUses int) (int64, error)
	EnrollmentTokens(ctx context.Context) ([]identity.EnrollmentToken, error)
	RevokeEnrollmentToken(ctx context.Context, id int64) (bool, error)
	// Usuários locais pelo painel (Aula 15.3): os mesmos do patchd-server user.
	CreatePanelUser(ctx context.Context, u panel.User) error
	PanelUsers(ctx context.Context) ([]panel.User, error)
	SetPanelPassword(ctx context.Context, name, hash string) (bool, error)
	DeletePanelUser(ctx context.Context, name string) (bool, error)
	// Sessões abertas e contagem dos jobs no banco (Aula 15.4).
	PanelSessions(ctx context.Context) ([]store.PanelSessionRow, error)
	// Trilha de auditoria (Aula 15.13).
	RecordAudit(ctx context.Context, e store.AuditEvent) error
	AuditEvents(ctx context.Context, f store.AuditFilter) ([]store.AuditEvent, error)
	DeleteUserSessions(ctx context.Context, username string) (int64, error)
	JobCounts(ctx context.Context, since time.Time) ([]store.JobCount, error)
	JobsFinishedSince(ctx context.Context, states []string, since time.Time) (int, error)
	// Logotipo do painel (Aula 19.1).
	PanelAsset(ctx context.Context, name string) (store.PanelAsset, bool, error)
	SetPanelAsset(ctx context.Context, a store.PanelAsset) error
	DeletePanelAsset(ctx context.Context, name string) (bool, error)
}

// maxClockSkew é a diferença de relógio a partir da qual o servidor registra um aviso.
const maxClockSkew = 5 * time.Minute

// machineIDPattern vale para as rotas de leitura: aceita os UUIDs emitidos no enrollment
// e os IDs antigos da Aula 4.1, que continuam no banco.
var machineIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// hashPattern: o formato do hash do protocolo ("sha256:" + 64 dígitos hexadecimais).
var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type api struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time

	releases      release.Dir // vazia: sem atualização automática
	serverVersion string

	contentDir string        // vazio: o servidor não distribui o catálogo offline
	content    ContentSource // a versão atual de cada arquivo distribuído

	repoCache *protocol.RepoCache // nil: o check-in não anuncia o cache de repositórios

	directory   Directory                  // nil: só usuários locais
	compliance  ComplianceSource           // nil: sem estado de compliance (armazenamento em memória)
	tpManifest  *thirdparty.Manifest       // regras de terceiros, para conferir o job thirdparty (Aula 14.4)
	jobKey      ed25519.PrivateKey         // nil: o painel mostra os jobs, mas não cria (Aula 8.2)
	publicURL   string                     // URL HTTPS anunciada aos agentes (Aula 9.2); vazio: sem botão de troca
	timezone    string                     // fuso sugerido na janela dos anéis (Aula 11.1)
	brand       string                     // nome no topo do painel (Aula 11.3)
	oidc        *oidc.Provider             // login pelo RHBK (Aula 9.4); nil: desligado
	oidcLogins  oidcPending                // logins pelo RHBK em andamento (state → nonce e PKCE)
	metrics     *Metrics                   // contadores da Aula 10.2; nil: desligados
	limiter     loginLimiter               // falhas de login por usuário+IP e por IP
	agentLimits agentLimiter               // requisições por máquina (Aula 20.5)
	hashSlots   chan struct{}              // conferências de senha simultâneas
	system      *SystemInfo                // Configuração › Sistema (Aula 15.2); nil: só o que a API sabe
	agentCA     []byte                     // PATCHD_AGENT_CA_FILE: a CA oferecida na aba Agente (Aula 15.2)
	adInfo      *ADInfo                    // Configuração › Acesso: as variáveis do AD e o teste (Aula 15.3)
	notifier    *notifierState             // e-mail para a equipe (Aula 18.5); nil: desligado
	chrome      chromeCache                // contadores do menu e logotipo (Aula 19.1)
	params      Settings                   // parâmetros pela tela (Aula 19.2); nil: só o .env
	repoCacheFn func() *protocol.RepoCache // anúncio do cache calculado a cada check-in (Aula 19.2)
}

// New monta o handler da API, já com ID de requisição, log de acesso e recuperação de pânico.
func New(st Store, logger *slog.Logger, opts ...Option) http.Handler {
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() }, brand: "patchd",
		hashSlots: make(chan struct{}, loginConcurrent)}
	for _, o := range opts {
		o(a)
	}

	mux := http.NewServeMux()
	// Agente.
	mux.HandleFunc("POST /api/v1/enroll", a.enroll)
	mux.Handle("POST /api/v1/agent/checkin", a.machineAuth(a.checkin))
	mux.Handle("POST /api/v1/agent/inventory", a.machineAuth(a.submitInventory))
	mux.Handle("POST /api/v1/agent/scan", a.machineAuth(a.submitScan))
	mux.Handle("GET /api/v1/agent/releases/{version}/{file}", a.machineAuth(a.releaseFile))
	mux.Handle("GET "+protocol.OfflineCatalogPath+"{sha256}", a.machineAuth(a.offlineCatalogFile))
	mux.Handle("GET "+protocol.PackagesPath+"{file}", a.machineAuth(a.packageFile))
	mux.Handle("POST "+protocol.JobResultPath+"{id}/result", a.machineAuth(a.jobResult))
	// Autoatendimento (Aula 17.2).
	mux.Handle("POST "+protocol.SelfServiceCatalogPath, a.machineAuth(a.selfServiceCatalog))
	mux.Handle("POST "+protocol.SelfServiceRequestPath, a.machineAuth(a.selfServiceRequest))
	mux.Handle("GET "+protocol.SelfServiceLogoPath, a.machineAuth(a.selfServiceLogo)) // Aula 19.1b
	// Painel (Aula 7.2).
	mux.HandleFunc("GET /painel/favicon.svg", favicon)
	mux.HandleFunc("GET /painel/estilo.css", styleFile) // Aula 20.4: sem sessão, a tela de login também usa
	mux.HandleFunc("GET /painel/logo", a.logoFile)      // Aula 19.1: sem sessão, a tela de login também mostra
	mux.HandleFunc("GET /painel/entrar", a.loginForm)
	mux.Handle("POST /painel/entrar", a.sameOrigin(a.login))
	mux.Handle("POST /painel/sair", a.sameOrigin(a.logout))
	mux.HandleFunc("GET /painel/oidc/entrar", a.oidcStart)
	mux.HandleFunc("GET /painel/oidc/retorno", a.oidcReturn)
	mux.Handle("GET /painel/{$}", a.panelPage(a.fleetPage))
	mux.Handle("GET /painel/maquinas/{id}", a.panelPage(a.machinePage))
	mux.Handle("GET /painel/inventario", a.panelPage(a.equipmentPage))
	mux.Handle("GET /painel/inventario.csv", a.panelPage(a.equipmentCSV))
	mux.Handle("GET /painel/patch", a.panelPage(a.patchPage))
	mux.Handle("GET /painel/patch/item", a.panelPage(a.patchItemPage))
	mux.Handle("GET /painel/patch/programa", a.panelPage(a.thirdPartyProgramPage))
	mux.Handle("POST /painel/patch/terceiros/aprovar", a.sameOrigin(a.adminAction(a.approveThirdPartyAction)))
	mux.Handle("POST /painel/patch/terceiros/revogar", a.sameOrigin(a.adminAction(a.revokeThirdPartyAction)))
	mux.Handle("POST /painel/patch/aprovar", a.sameOrigin(a.adminAction(a.approvePatchAction)))
	mux.Handle("POST /painel/patch/revogar", a.sameOrigin(a.adminAction(a.revokePatchAction)))
	mux.Handle("POST /painel/patch/aprovar-lote", a.sameOrigin(a.adminAction(a.approveBatchAction)))
	mux.Handle("POST /painel/patch/correcao", a.sameOrigin(a.adminAction(a.approveFixAction)))
	mux.Handle("GET /painel/aposentadas", a.panelPage(a.retiredPage))
	mux.Handle("GET /painel/frota.csv", a.panelPage(a.fleetCSV))
	mux.Handle("GET /painel/maquinas/{id}/pendencias.csv", a.panelPage(a.itemsCSV))
	mux.Handle("POST /painel/maquinas/{id}/aposentar", a.sameOrigin(a.adminAction(a.retireAction)))
	mux.Handle("POST /painel/maquinas/{id}/restaurar", a.sameOrigin(a.adminAction(a.restoreAction)))
	mux.Handle("GET /painel/jobs", a.panelPage(a.jobsPage))
	mux.Handle("GET /painel/autoatendimento", a.panelPage(a.selfServiceReportPage))                                                        // Aula 17.7
	mux.Handle("POST /painel/autoatendimento/pedidos/{id}/aprovar", a.sameOrigin(a.adminAction(a.staleAfter(a.approveSelfServiceAction)))) // Aula 18.3
	mux.Handle("POST /painel/autoatendimento/pedidos/{id}/recusar", a.sameOrigin(a.adminAction(a.staleAfter(a.refuseSelfServiceAction))))
	mux.Handle("GET /painel/campanhas", a.panelPage(a.campaignsPage))
	mux.Handle("POST /painel/campanhas/nova", a.sameOrigin(a.adminAction(a.newCampaignAction)))
	mux.Handle("GET /painel/campanhas/{id}", a.panelPage(a.campaignPage))
	mux.Handle("POST /painel/campanhas/{id}/{op}", a.sameOrigin(a.adminAction(a.staleAfter(a.campaignChangeAction))))
	mux.Handle("GET /painel/aneis", a.panelPage(a.ringsPage))
	mux.Handle("POST /painel/aneis/janela", a.sameOrigin(a.adminAction(a.setWindowAction)))
	mux.Handle("POST /painel/aneis/{ring}/remover-janela", a.sameOrigin(a.adminAction(a.deleteWindowAction)))
	mux.Handle("POST /painel/aneis/maquinas", a.sameOrigin(a.adminAction(a.fleetRingAction)))
	mux.Handle("POST /painel/maquinas/{id}/anel", a.sameOrigin(a.adminAction(a.machineRingAction)))
	mux.Handle("POST /painel/maquinas/{id}/buscar", a.sameOrigin(a.adminAction(a.rescanAction)))
	mux.Handle("POST /painel/jobs/{id}/cancelar", a.sameOrigin(a.adminAction(a.cancelJobAction)))
	mux.Handle("POST /painel/maquinas/{id}/atualizar", a.sameOrigin(a.adminAction(a.updateAction)))
	mux.Handle("POST /painel/maquinas/{id}/reiniciar", a.sameOrigin(a.adminAction(a.rebootAction)))
	mux.Handle("POST /painel/maquinas/{id}/windows/instalar", a.sameOrigin(a.adminAction(a.winUpdateAction))) // Aula 16.3
	mux.Handle("POST /painel/maquinas/{id}/https", a.sameOrigin(a.adminAction(a.serverURLAction)))
	mux.Handle("POST /painel/maquinas/{id}/terceiros/atualizar", a.sameOrigin(a.adminAction(a.thirdPartyUpdateAction)))
	// Configuração (Aula 15.2): só admin, inclusive para ver.
	mux.Handle("GET /painel/configuracao", a.adminAction(a.settingsPage))
	mux.Handle("GET /painel/configuracao/auditoria.csv", a.adminAction(a.auditCSV))
	mux.Handle("GET /painel/configuracao/agente/arquivo/{name}", a.adminAction(a.agentFile))
	mux.Handle("GET /painel/configuracao/agente/ca.crt", a.adminAction(a.agentCAFile))
	mux.Handle("POST /painel/configuracao/agente/token", a.sameOrigin(a.adminAction(a.createTokenAction)))
	mux.Handle("POST /painel/configuracao/agente/token/{id}/revogar", a.sameOrigin(a.adminAction(a.revokeTokenAction)))
	mux.Handle("POST /painel/configuracao/parametros", a.sameOrigin(a.adminAction(a.paramsAction)))                // Aula 19.2
	mux.Handle("POST /painel/configuracao/parametros/restaurar", a.sameOrigin(a.adminAction(a.paramsResetAction))) // Aula 19.2
	mux.Handle("POST /painel/configuracao/aparencia/logo", a.sameOrigin(a.adminAction(a.logoAction)))              // Aula 19.1
	mux.Handle("POST /painel/configuracao/notificacoes/teste", a.sameOrigin(a.adminAction(a.notifyTestAction)))
	mux.Handle("POST /painel/configuracao/autoatendimento", a.sameOrigin(a.adminAction(a.selfServiceOfferAction))) // Aula 17.1
	mux.Handle("POST /painel/configuracao/acesso/testar-ad", a.sameOrigin(a.adminAction(a.testADAction)))
	mux.Handle("POST /painel/configuracao/acesso/testar-oidc", a.sameOrigin(a.adminAction(a.testOIDCAction)))
	mux.Handle("POST /painel/configuracao/acesso/usuarios", a.sameOrigin(a.adminAction(a.createLocalUserAction)))
	mux.Handle("POST /painel/configuracao/acesso/usuarios/{name}/senha", a.sameOrigin(a.adminAction(a.resetLocalPasswordAction)))
	mux.Handle("POST /painel/configuracao/acesso/usuarios/{name}/remover", a.sameOrigin(a.adminAction(a.deleteLocalUserAction)))
	mux.Handle("POST /painel/configuracao/acesso/sessoes/{id}/encerrar", a.sameOrigin(a.adminAction(a.endSessionAction)))
	mux.Handle("POST /painel/configuracao/acesso/sessoes/usuario", a.sameOrigin(a.adminAction(a.endUserSessionsAction)))
	// Leitura: exigem a sessão do painel.
	mux.Handle("GET /api/v1/machines/{id}/inventory", a.sessionAuth(a.getInventory))
	mux.Handle("GET /api/v1/machines/{id}/scan", a.sessionAuth(a.getScan))
	mux.Handle("GET /api/v1/machines", a.sessionAuth(a.listMachines))
	mux.Handle("GET /api/v1/identity-links", a.sessionAuth(a.listIdentityLinks))
	mux.Handle("GET /api/v1/machines/{id}/compliance", a.sessionAuth(a.machineCompliance))
	mux.Handle("GET /api/v1/compliance", a.sessionAuth(a.fleetCompliance))

	// Ordem: o ID existe antes do log; o log enxerga o 500 produzido pela recuperação.
	return withRequestID(withAccessLog(logger, a.observe, withRecover(logger, SecurityHeaders(mux))))
}

// machineID lê e valida o {id} da URL nas rotas de leitura. Em caso de erro, já respondeu.
func (a *api) machineID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_machine_id", "ID de máquina inválido")
		return "", false
	}
	return id, true
}

// submitInventory recebe o inventário da máquina autenticada.
func (a *api) submitInventory(w http.ResponseWriter, r *http.Request, id string) {
	var rep protocol.InventoryReport
	if !decodeJSON(w, r, &rep) {
		return
	}
	if rep.SchemaVersion != protocol.InventorySchemaVersion {
		writeError(w, http.StatusUnprocessableEntity, "unsupported_schema",
			"schema_version do inventário não suportado (este servidor aceita a versão 1)")
		return
	}
	if !hashPattern.MatchString(rep.Hash) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_hash", "hash ausente ou fora do formato sha256:<64 hex>")
		return
	}
	if rep.OS.Family == "" {
		writeError(w, http.StatusUnprocessableEntity, "missing_os", "o relatório não identifica o sistema operacional")
		return
	}
	// Aula 21.1: o firmware só entra com os valores conhecidos; fora disso, fica de fora (o
	// resto do inventário segue) e o motivo vai para os erros do relatório.
	if hw := rep.Hardware; hw != nil && hw.Firmware != nil {
		if err := hw.Firmware.Check(); err != nil {
			a.logger.Warn("inventário: firmware descartado", "machine_id", id, "error", err)
			hw.Firmware = nil
			rep.Errors = append(rep.Errors, "firmware: descartado pelo servidor: "+err.Error())
		}
	}

	stored, err := a.store.SaveInventory(r.Context(), id, rep)
	if err != nil {
		a.internalError(w, r, "gravar inventário", err)
		return
	}
	a.checkClock(r, id, rep.CollectedAt)

	status, code := "unchanged", http.StatusOK
	if stored {
		status, code = "stored", http.StatusCreated
	}
	writeJSON(w, code, protocol.SubmitResponse{Status: status, Hash: rep.Hash, ServerTime: a.now()})
}

// submitScan recebe a busca de atualizações da máquina autenticada.
func (a *api) submitScan(w http.ResponseWriter, r *http.Request, id string) {
	var rep protocol.PatchScanReport
	if !decodeJSON(w, r, &rep) {
		return
	}
	if rep.SchemaVersion != protocol.PatchSchemaVersion {
		writeError(w, http.StatusUnprocessableEntity, "unsupported_schema",
			"schema_version da busca não suportado (este servidor aceita a versão 2)")
		return
	}
	if err := a.store.SaveScan(r.Context(), id, rep); err != nil {
		a.internalError(w, r, "gravar busca", err)
		return
	}
	a.checkClock(r, id, rep.ScannedAt)
	writeJSON(w, http.StatusCreated, protocol.SubmitResponse{Status: "stored", ServerTime: a.now()})
}

func (a *api) getInventory(w http.ResponseWriter, r *http.Request) {
	id, ok := a.machineID(w, r)
	if !ok {
		return
	}
	rep, found, err := a.store.LatestInventory(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler inventário", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "nenhum inventário recebido desta máquina")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) getScan(w http.ResponseWriter, r *http.Request) {
	id, ok := a.machineID(w, r)
	if !ok {
		return
	}
	rep, found, err := a.store.LatestScan(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler busca", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "nenhuma busca recebida desta máquina")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) listMachines(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) listIdentityLinks(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.IdentityLinks(r.Context())
	if err != nil {
		a.internalError(w, r, "listar alertas de identidade", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// checkClock registra quando o horário da coleta diverge do relógio do servidor
// (decisão da Aula 0.1: o servidor calcula o desvio).
func (a *api) checkClock(r *http.Request, id string, collectedAt time.Time) {
	if collectedAt.IsZero() {
		return
	}
	skew := a.now().Sub(collectedAt)
	if skew > maxClockSkew || skew < -maxClockSkew {
		a.logger.Warn("relógio do agente divergente do servidor",
			"machine_id", id, "skew_seconds", int64(skew.Seconds()), "request_id", RequestID(r.Context()))
	}
}

// internalError registra a causa e responde 500 sem expor detalhes internos ao cliente.
func (a *api) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	a.logger.Error("falha interna", "op", op, "error", err, "request_id", RequestID(r.Context()))
	writeError(w, http.StatusInternalServerError, "internal", "erro interno do servidor")
}
