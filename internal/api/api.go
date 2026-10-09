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
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/release"
)

// Store é o armazenamento de que a API precisa. Definido aqui, onde é usado: qualquer
// tipo com estes métodos serve (a memória, o PostgreSQL, um fake de teste).
type Store interface {
	SaveInventory(ctx context.Context, machineID string, rep protocol.InventoryReport) (stored bool, err error)
	LatestInventory(ctx context.Context, machineID string) (protocol.InventoryReport, bool, error)
	SaveScan(ctx context.Context, machineID string, rep protocol.PatchScanReport) error
	LatestScan(ctx context.Context, machineID string) (protocol.PatchScanReport, bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	// Enroll consome um uso do token e registra a máquina; token inválido devolve
	// um erro que satisfaz errors.Is(err, identity.ErrEnrollmentRejected).
	// Também cria os alertas de identidade contra as máquinas já conhecidas e os devolve.
	Enroll(ctx context.Context, tokenHash []byte, m identity.NewMachine) ([]protocol.IdentityLink, error)
	MachineByCredential(ctx context.Context, credentialHash []byte) (machineID string, found bool, err error)
	IdentityLinks(ctx context.Context) ([]protocol.IdentityLink, error)
	// CheckIn registra o contato periódico e diz se o inventário atual tem o hash informado.
	CheckIn(ctx context.Context, machineID, agentVersion, inventoryHash string) (inventoryKnown bool, err error)
	// Usuários e sessões do painel.
	PanelStore
	// Máquinas aposentadas (Aula 7.5): o painel lista, aposenta e restaura (Aula 7.6).
	RetiredMachines(ctx context.Context) ([]protocol.RetiredMachine, error)
	RetireMachine(ctx context.Context, id, reason, by string) (bool, error)
	RestoreMachine(ctx context.Context, id string) (bool, error)
	// Fila de jobs (Aula 8.1).
	JobStore
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

	directory  Directory        // nil: só usuários locais
	compliance ComplianceSource // nil: sem estado de compliance (armazenamento em memória)
	limiter    loginLimiter     // falhas de login por usuário+IP e por IP
	hashSlots  chan struct{}    // conferências de senha simultâneas
}

// New monta o handler da API, já com ID de requisição, log de acesso e recuperação de pânico.
func New(st Store, logger *slog.Logger, opts ...Option) http.Handler {
	a := &api{store: st, logger: logger, now: func() time.Time { return time.Now().UTC() },
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
	mux.Handle("POST "+protocol.JobResultPath+"{id}/result", a.machineAuth(a.jobResult))
	// Painel (Aula 7.2).
	mux.HandleFunc("GET /painel/entrar", a.loginForm)
	mux.Handle("POST /painel/entrar", a.sameOrigin(a.login))
	mux.Handle("POST /painel/sair", a.sameOrigin(a.logout))
	mux.Handle("GET /painel/{$}", a.panelPage(a.fleetPage))
	mux.Handle("GET /painel/maquinas/{id}", a.panelPage(a.machinePage))
	mux.Handle("GET /painel/aposentadas", a.panelPage(a.retiredPage))
	mux.Handle("GET /painel/frota.csv", a.panelPage(a.fleetCSV))
	mux.Handle("GET /painel/maquinas/{id}/pendencias.csv", a.panelPage(a.itemsCSV))
	mux.Handle("POST /painel/maquinas/{id}/aposentar", a.sameOrigin(a.adminAction(a.retireAction)))
	mux.Handle("POST /painel/maquinas/{id}/restaurar", a.sameOrigin(a.adminAction(a.restoreAction)))
	// Leitura: exigem a sessão do painel.
	mux.Handle("GET /api/v1/machines/{id}/inventory", a.sessionAuth(a.getInventory))
	mux.Handle("GET /api/v1/machines/{id}/scan", a.sessionAuth(a.getScan))
	mux.Handle("GET /api/v1/machines", a.sessionAuth(a.listMachines))
	mux.Handle("GET /api/v1/identity-links", a.sessionAuth(a.listIdentityLinks))
	mux.Handle("GET /api/v1/machines/{id}/compliance", a.sessionAuth(a.machineCompliance))
	mux.Handle("GET /api/v1/compliance", a.sessionAuth(a.fleetCompliance))

	// Ordem: o ID existe antes do log; o log enxerga o 500 produzido pela recuperação.
	return withRequestID(withAccessLog(logger, withRecover(logger, mux)))
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
