package selfservice

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// Store é o que o autoatendimento lê e grava.
type Store interface {
	jobs.Queue
	SelfServiceOffers(ctx context.Context) ([]store.SelfServiceOffer, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	ThirdPartyApprovals(ctx context.Context) ([]store.ThirdPartyApproval, error)
	LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error)
	HostState(ctx context.Context, id string) (store.HostState, bool, error)
	Jobs(ctx context.Context, machineID string, limit int) ([]store.JobRow, error)
	JobForMachine(ctx context.Context, machineID string, id int64) (store.JobRow, bool, error)
	LatestScan(ctx context.Context, id string) (protocol.PatchScanReport, bool, error)
	// Aula 18.3: os pedidos que esperam a TI.
	CreateSelfServiceRequest(ctx context.Context, q store.SelfServiceRequest) (int64, error)
	SelfServiceRequests(ctx context.Context, f store.RequestFilter) ([]store.SelfServiceRequest, error)
	SelfServiceRequestByID(ctx context.Context, id int64) (store.SelfServiceRequest, bool, error)
	DecideSelfServiceRequest(ctx context.Context, id int64, state, by, note string, jobID int64, at time.Time) error
	ExpireSelfServiceRequests(ctx context.Context, before, now time.Time) (int64, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
	// As capacidades do agente (o programa por usuário só vai a quem instala como o usuário).
	MachineCapabilities(ctx context.Context, id string) ([]string, error)
}

// Service atende a loja e os pedidos (Aula 17.2). A API do agente e a linha de comando usam
// o mesmo serviço: o mesmo pedido dá a mesma decisão.
type Service struct {
	Store       Store
	Manifest    *thirdparty.Live
	Key         ed25519.PrivateKey
	PackagesDir string // PATCHD_CONTENT_DIR/pacotes
	Now         func() time.Time
	// PlanOptions: as opções do plano da máquina (a regularização). nil: o plano comum.
	PlanOptions func(ctx context.Context, machineID string) (tpinstall.Options, error)
	// Windows: as atualizações do Windows que o usuário pode instalar nesta máquina e o job
	// que as instala (nil: desligado, sem Windows ou sem busca). Quem monta é a API, pelo
	// mesmo caminho do botão do painel: só KBs aprovados em Patch e pendentes na busca.
	Windows func(ctx context.Context, machineID string) (*WindowsPlan, error)
}

// WindowsPlan: o que a Central mostra e o job que instala.
type WindowsPlan struct {
	State  protocol.SelfServiceWindowsState
	Params jobs.WinUpdateParams
}

// WindowsName: o nome das atualizações do Windows em Meus pedidos.
const WindowsName = "Atualizações do Windows"

// planOptions: as opções da máquina, ou as do plano comum.
func (s *Service) planOptions(ctx context.Context, machineID string) (tpinstall.Options, error) {
	if s.PlanOptions == nil {
		return tpinstall.Options{}, nil
	}
	return s.PlanOptions(ctx, machineID)
}

// ErrOff: o servidor está sem o que o autoatendimento precisa (regras de terceiros, chave
// de jobs). A API responde 503.
var ErrOff = errors.New("autoatendimento desligado neste servidor")

// JobTTL: o prazo do job pedido pelo usuário. O agente pede o check-in na hora (17.3); o
// prazo cobre a máquina que desliga logo depois do pedido.
const JobTTL = 24 * time.Hour

// openJobsScan: quantos jobs recentes da máquina são olhados atrás de um em aberto.
const openJobsScan = 50

// Catalog monta a loja do usuário na máquina, com a instalação em andamento, se houver.
func (s *Service) Catalog(ctx context.Context, machineID string, u protocol.SelfServiceUser) (protocol.SelfServiceCatalog, error) {
	out := protocol.SelfServiceCatalog{Items: []protocol.SelfServiceItem{}, GeneratedAt: s.Now().UTC()}
	items, err := s.items(ctx, machineID, u)
	if err != nil {
		return out, err
	}
	waiting, err := s.userRequests(ctx, machineID, u.Account)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		x := protocol.SelfServiceItem{App: it.AppID, Name: it.Name, Installed: it.Installed, Target: it.Target,
			Action: it.Action, Reason: it.Reason, Approval: it.NeedsApproval, Replaces: it.Replaces, Category: it.Category, PerUser: it.PerUser}
		for _, q := range waiting {
			if q.AppID == it.AppID && q.State == store.RequestWaiting {
				x.Pending = q.ID
				break
			}
		}
		out.Items = append(out.Items, x)
	}
	if out.OpenJob, err = s.openJob(ctx, machineID); err != nil {
		return out, err
	}
	if out.Requests, err = s.requests(ctx, machineID, u.Account); err != nil {
		return out, err
	}
	if err := s.closeOffers(ctx, machineID, &out); err != nil {
		return out, err
	}
	if out.Windows, err = s.windowsState(ctx, machineID); err != nil {
		return out, err
	}
	out.Reboot, err = s.rebootState(ctx, machineID, u.Account)
	return out, err
}

// MaxRequests: quantos pedidos do usuário a loja mostra.
const MaxRequests = 10

// Author é o autor dos jobs pedidos pelo usuário (o mesmo texto que aparece no painel).
func Author(account string) string { return "autoatendimento: " + account }

// requests são os pedidos recentes do usuário nesta máquina (Aula 17.5), entre os últimos
// jobs dela. O nome e a versão vêm dos parâmetros assinados do job.
func (s *Service) requests(ctx context.Context, machineID, account string) ([]protocol.SelfServiceJob, error) {
	list, err := s.Store.Jobs(ctx, machineID, openJobsScan)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if s.Manifest != nil {
		for _, r := range s.Manifest.For("windows") {
			names[r.ID] = r.Name
		}
	}
	out := []protocol.SelfServiceJob{}
	for _, j := range list {
		if len(out) == MaxRequests {
			break
		}
		if (j.Type != jobs.TypeThirdParty && j.Type != jobs.TypeReboot && j.Type != jobs.TypeWinUpdate) || !strings.EqualFold(j.CreatedBy, Author(account)) {
			continue
		}
		if j.Type == jobs.TypeWinUpdate {
			item := protocol.SelfServiceJob{ID: j.ID, App: protocol.SelfServiceWindows, Name: WindowsName, State: j.State,
				CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Detail: short(j.Detail, MaxJobDetail)}
			if full, found, err := s.Store.JobForMachine(ctx, machineID, j.ID); err != nil {
				return nil, err
			} else if found {
				var payload jobs.Job
				if json.Unmarshal([]byte(full.Payload), &payload) == nil {
					if p, err := jobs.ParseWinUpdateParams(payload.Params); err == nil {
						item.Version = strings.Join(p.Labels(), ", ")
					}
				}
			}
			out = append(out, item)
			continue
		}
		if j.Type == jobs.TypeReboot { // Aula 17.6
			out = append(out, protocol.SelfServiceJob{ID: j.ID, App: "reinicio", Name: RebootName, State: j.State,
				CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Detail: short(j.Detail, MaxJobDetail)})
			continue
		}
		full, found, err := s.Store.JobForMachine(ctx, machineID, j.ID)
		if err != nil {
			return nil, err
		}
		item := protocol.SelfServiceJob{ID: j.ID, State: j.State, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Detail: short(j.Detail, MaxJobDetail)}
		var payload jobs.Job
		if found && json.Unmarshal([]byte(full.Payload), &payload) == nil {
			if p, err := jobs.ParseThirdPartyParams(payload.Params); err == nil {
				item.App, item.Version = p.App, p.Version
			}
		}
		item.Name = names[item.App]
		if item.Name == "" {
			item.Name = item.App
		}
		out = append(out, item)
	}
	// Aula 18.3: os pedidos que passaram (ou passam) pela TI e não viraram job. Os aprovados
	// já aparecem pelo job.
	qs, err := s.userRequests(ctx, machineID, account)
	if err != nil {
		return nil, err
	}
	for _, q := range qs {
		if q.State == store.RequestApproved {
			continue
		}
		item := protocol.SelfServiceJob{Request: q.ID, App: q.AppID, Name: names[q.AppID], Version: q.Version,
			State: RequestStateLabels[q.State], CreatedAt: q.CreatedAt, FinishedAt: q.DecidedAt}
		if item.Name == "" {
			item.Name = q.AppID
		}
		if q.State == store.RequestRefused && q.Note != "" {
			item.Detail = "motivo: " + short(q.Note, 200)
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b protocol.SelfServiceJob) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(out) > MaxRequests {
		out = out[:MaxRequests]
	}
	return out, nil
}

// RequestStateLabels: o estado do pedido que espera a TI, como vai para o app. Texto legível
// de propósito: o app das versões até a v0.17.4 mostra o estado como vem.
var RequestStateLabels = map[string]string{
	store.RequestWaiting: "aguardando aprovação",
	store.RequestRefused: "recusado pela TI",
	store.RequestExpired: "expirado sem aprovação",
}

// MaxNote: o motivo da recusa.
const MaxNote = 200

// MaxJobDetail: o detalhe do job no app (o resultado do agente, com o motivo da falha e o
// caminho do log). Inteiro na prática: o app quebra a linha.
const MaxJobDetail = 1500

// userRequests: os pedidos do usuário na máquina que passaram pela TI, depois de expirar os
// velhos.
func (s *Service) userRequests(ctx context.Context, machineID, account string) ([]store.SelfServiceRequest, error) {
	if _, err := s.Store.ExpireSelfServiceRequests(ctx, s.Now().Add(-store.RequestTTL), s.Now()); err != nil {
		return nil, err
	}
	return s.Store.SelfServiceRequests(ctx, store.RequestFilter{MachineID: machineID, Account: account, Limit: MaxRequests})
}

// short corta o texto em n letras.
func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (s *Service) items(ctx context.Context, machineID string, u protocol.SelfServiceUser) ([]Item, error) {
	if s.Manifest == nil {
		return nil, ErrOff
	}
	inv, found, err := s.Store.LatestInventory(ctx, machineID)
	if err != nil {
		return nil, err
	}
	arch, ok := tpinstall.MachineArch(inv.OS.Arch)
	if !found || inv.OS.ID != "windows" || !ok {
		return nil, nil // sem inventário do Windows: loja vazia
	}
	offers, err := s.Store.SelfServiceOffers(ctx)
	if err != nil {
		return nil, err
	}
	pkgs, err := s.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, err
	}
	approvals, err := s.Store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, err
	}
	opts, err := s.planOptions(ctx, machineID)
	if err != nil {
		return nil, err
	}
	m := Machine{Software: inv.Software, Arch: arch, Options: opts, UserSID: u.SID}
	if slices.ContainsFunc(s.Manifest.For("windows"), thirdparty.App.PerUser) {
		caps, err := s.Store.MachineCapabilities(ctx, machineID)
		if err != nil {
			return nil, err
		}
		m.CanUserInstall = slices.Contains(caps, protocol.CapabilityUserInstall)
	}
	return Catalog(offers, TokenMember(u.Groups), m, s.Manifest.Get(), pkgs, approvals), nil
}

func (s *Service) openJob(ctx context.Context, machineID string) (int64, error) {
	list, err := s.Store.Jobs(ctx, machineID, openJobsScan)
	if err != nil {
		return 0, err
	}
	for _, j := range list {
		if j.Type == jobs.TypeThirdParty && !jobs.Final(j.State) && j.NotAfter.After(s.Now()) {
			return j.ID, nil
		}
	}
	return 0, nil
}

// Outcome é o que aconteceu com o pedido, para a auditoria de quem chamou.
type Outcome struct {
	Response protocol.SelfServiceResponse
	Job      *jobs.Record
	// RequestID (Aula 18.3): o pedido guardado para a TI aprovar (0: não passou pela TI).
	RequestID int64
	Version   string
	Seen      bool   // o usuário aparece entre os conectados da última coleta (15.6)
	Why       string // recusado: o motivo para o log (a mensagem do usuário é mais curta)
}

// Request decide o pedido. A loja é refeita aqui: o usuário só leva o que ela mostra agora,
// com a ação que ela mostra. Recusas são respostas (status recusado/aguarde), não erros.
func (s *Service) Request(ctx context.Context, machineID string, req protocol.SelfServiceRequest) (Outcome, error) {
	var o Outcome
	refuse := func(why, msg string) (Outcome, error) {
		o.Why = why
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceRefused, Message: msg}
		return o, nil
	}
	if s.Key == nil {
		return o, ErrOff
	}
	seen, err := s.seen(ctx, machineID, req.Account)
	if err != nil {
		return o, err
	}
	o.Seen = seen
	if req.Action == protocol.SelfServiceReboot {
		return s.reboot(ctx, machineID, req, o)
	}
	if req.Action == protocol.SelfServiceWindows {
		return s.windows(ctx, machineID, req, o)
	}
	if req.Action != protocol.SelfServiceUpdate && req.Action != protocol.SelfServiceInstall {
		return refuse("ação desconhecida", "Pedido inválido.")
	}
	items, err := s.items(ctx, machineID, req.SelfServiceUser)
	if err != nil {
		return o, err
	}
	i := slices.IndexFunc(items, func(it Item) bool { return it.AppID == req.App })
	if i < 0 || items[i].Action != req.Action {
		return refuse("fora da loja do usuário (ou outra ação)", "Este programa não está disponível para você nesta máquina agora.")
	}
	it := items[i]
	if it.NeedsApproval {
		return s.requestApproval(ctx, machineID, req, it, o)
	}
	open, err := s.openJob(ctx, machineID)
	if err != nil {
		return o, err
	}
	if open > 0 {
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceWait, JobID: open,
			Message: "Já há uma instalação em andamento nesta máquina. Tente de novo quando ela terminar."}
		o.Why = fmt.Sprintf("job %d em aberto", open)
		return o, nil
	}
	p, why, err := s.params(ctx, machineID, it, req.SelfServiceUser)
	if err != nil {
		return o, err
	}
	if why != "" {
		return refuse(why, "Este programa está indisponível agora. Avise o suporte.")
	}
	if req.Close {
		// "Fechar o programa e instalar": quem pediu já confirmou; o agente fecha só o da sessão dele.
		ok, err := s.canCloseForUser(ctx, machineID, p)
		if err != nil {
			return o, err
		}
		if !ok {
			return refuse("fechar pelo usuário: sem a lista de processos, ou agente sem a capacidade",
				"O patchd não consegue fechar este programa neste computador. Feche-o você mesmo e peça de novo.")
		}
		p.Close = &jobs.CloseApps{User: req.Account, UserSID: req.SID}
		if err := p.Validate(); err != nil {
			return refuse("fechar pelo usuário: "+err.Error(), "Pedido inválido.")
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return o, err
	}
	rec, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: machineID, Type: jobs.TypeThirdParty, TTL: JobTTL,
		By: Author(req.Account), Params: raw}, s.Now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		return refuse("máquina aposentada", "Esta máquina não está mais no parque.")
	}
	if err != nil {
		return o, err
	}
	o.Job, o.Version = &rec, p.Version
	verb := "instalação"
	if req.Action == protocol.SelfServiceUpdate {
		verb = "atualização"
	}
	msg := fmt.Sprintf("Pedido aceito: %s de %s %s. Feche o programa, se estiver aberto; o patchd avisa quando terminar.", verb, it.Name, p.Version)
	if p.Close != nil {
		msg = fmt.Sprintf("Pedido aceito: o patchd vai fechar %s (o que não estiver salvo nele se perde) e fazer a %s para a versão %s. Ele avisa quando terminar.",
			it.Name, verb, p.Version)
	}
	o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: rec.ID, Message: msg}
	return o, nil
}

// params monta o job pelo mesmo caminho do painel: a decisão da máquina com os instaladores
// aprovados, conferidos no cache.
func (s *Service) params(ctx context.Context, machineID string, it Item, u protocol.SelfServiceUser) (jobs.ThirdPartyParams, string, error) {
	pkgs, err := s.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return jobs.ThirdPartyParams{}, "", err
	}
	inv, _, err := s.Store.LatestInventory(ctx, machineID)
	if err != nil {
		return jobs.ThirdPartyParams{}, "", err
	}
	arch, _ := tpinstall.MachineArch(inv.OS.Arch)
	var rule thirdparty.App
	for _, r := range s.Manifest.For("windows") {
		if r.ID == it.AppID {
			rule = r
		}
	}
	var versionPkgs []store.ThirdPartyPackage
	for _, p := range pkgs {
		if p.AppID == it.AppID && p.Version == it.Target {
			versionPkgs = append(versionPkgs, p)
		}
	}
	d := tpinstall.Decision{App: rule, State: tpinstall.StateUpdate}
	if rule.PerUser() {
		// No perfil de quem pediu: instalar e atualizar são o mesmo instalador, como ele.
		d.Package = tpinstall.ForUserInstall(rule, versionPkgs, arch)
	} else if it.Action == ActionInstall {
		d.Package = tpinstall.ForNewInstall(rule, versionPkgs, arch)
	} else {
		opts, err := s.planOptions(ctx, machineID)
		if err != nil {
			return jobs.ThirdPartyParams{}, "", err
		}
		for _, x := range tpinstall.PlanWith(inv.Software, s.Manifest.Get(), versionPkgs, arch, opts) {
			if x.App.ID == it.AppID && x.State == tpinstall.StateUpdate {
				d.Package, d.Regularize = x.Package, x.Regularize
			}
		}
	}
	if d.Package == nil {
		return jobs.ThirdPartyParams{}, "sem instalador da versão para esta máquina", nil
	}
	if s.PackagesDir == "" {
		return jobs.ThirdPartyParams{}, "o servidor não guarda instaladores (PATCHD_CONTENT_DIR)", nil
	}
	if _, err := pkgcache.Check(s.PackagesDir, d.Package.SHA256, d.Package.MSI()); err != nil {
		return jobs.ThirdPartyParams{}, "instalador ausente ou diferente no cache: " + err.Error(), nil
	}
	p, err := tpinstall.JobParams(d)
	if err != nil {
		return jobs.ThirdPartyParams{}, "parâmetros do instalador fora do job: " + err.Error(), nil
	}
	if rule.PerUser() {
		p.User, p.UserSID = u.Account, u.SID
		if err := p.Validate(); err != nil {
			return jobs.ThirdPartyParams{}, "usuário fora do job: " + err.Error(), nil
		}
	}
	return p, "", nil
}

// seen diz se o usuário aparece entre os conectados na última coleta da máquina (15.6). Não
// barra o pedido (a coleta é de hora em hora; quem acabou de entrar ainda não aparece): vai
// para a auditoria.
func (s *Service) seen(ctx context.Context, machineID, account string) (bool, error) {
	h, found, err := s.Store.HostState(ctx, machineID)
	if err != nil || !found || h.State == nil {
		return false, err
	}
	return slices.ContainsFunc(h.State.Users, func(u protocol.LoggedUser) bool { return strings.EqualFold(u.Account, account) }), nil
}

// RebootName é como o reinício aparece em "Meus pedidos".
const RebootName = "Reinício do computador"

// rebootTTL: o prazo do job de reinício conta a partir da hora escolhida.
const rebootTTL = time.Hour

// isServer: em servidor, o reinício derruba a sessão de todos; quem decide é a TI.
func isServer(os protocol.OSInfo) bool {
	return strings.Contains(os.Name, "Server") || strings.HasPrefix(os.Edition, "Server")
}

// rebootState é o reinício da máquina para o app (Aula 17.6): o pendente da última busca (o
// agente troca pelo de agora, que ele lê na hora) e o reinício já pedido, de qualquer autor.
func (s *Service) rebootState(ctx context.Context, machineID, account string) (*protocol.SelfServiceRebootState, error) {
	inv, found, err := s.Store.LatestInventory(ctx, machineID)
	if err != nil || !found || inv.OS.ID != "windows" {
		return nil, err
	}
	st := &protocol.SelfServiceRebootState{Allowed: !isServer(inv.OS)}
	rep, ok, err := s.Store.LatestScan(ctx, machineID)
	if err != nil {
		return nil, err
	}
	if ok && rep.Reboot != nil {
		st.Pending = rep.Reboot.Pending
		st.LastBoot = rep.Reboot.LastBoot
		if st.Pending != nil && *st.Pending {
			st.Reasons = protocol.RebootReasons(*rep.Reboot)
		}
	}
	list, err := s.Store.Jobs(ctx, machineID, openJobsScan)
	if err != nil {
		return nil, err
	}
	for _, j := range list {
		if j.Type != jobs.TypeReboot || jobs.Final(j.State) || !j.NotAfter.After(s.Now()) {
			continue
		}
		st.JobID = j.ID
		st.By = rebootAuthor(j.CreatedBy, account)
		full, found, err := s.Store.JobForMachine(ctx, machineID, j.ID)
		if err != nil {
			return nil, err
		}
		var payload jobs.Job
		if found && json.Unmarshal([]byte(full.Payload), &payload) == nil {
			if p, err := jobs.ParseRebootParams(payload.Params); err == nil {
				switch {
				case p.At != nil:
					at := *p.At
					st.At = &at
				case j.DeliveredAt != nil:
					at := j.DeliveredAt.Add(p.Delay())
					st.At = &at
				}
			}
		}
		break
	}
	if (st.Pending != nil && *st.Pending) || st.JobID > 0 {
		st.Updates, err = s.rebootUpdates(ctx, machineID, rep, ok, st.LastBoot, list)
		if err != nil {
			return nil, err
		}
	}
	return st, nil
}

// rebootLookback: sem a última inicialização, as atualizações destes dias.
const rebootLookback = 14 * 24 * time.Hour

// rebootUpdates: o que foi instalado desde a última inicialização, o mais novo primeiro: o
// histórico do Windows Update (da última busca) e os programas que o patchd instalou.
func (s *Service) rebootUpdates(ctx context.Context, machineID string, rep protocol.PatchScanReport, haveScan bool, lastBoot *time.Time,
	list []store.JobRow) ([]protocol.SelfServiceRebootUpdate, error) {
	since := s.Now().Add(-rebootLookback)
	if lastBoot != nil {
		since = *lastBoot
	}
	out := []protocol.SelfServiceRebootUpdate{}
	seen := map[string]bool{}
	if haveScan {
		for _, h := range rep.History {
			if h.Operation != "install" || !strings.HasPrefix(h.Result, "succeeded") || !h.Date.After(since) || h.Title == "" || seen[h.Title] {
				continue
			}
			seen[h.Title] = true
			d := h.Date
			out = append(out, protocol.SelfServiceRebootUpdate{Name: h.Title, Date: &d, Source: "Windows Update"})
		}
	}
	names := map[string]string{}
	if s.Manifest != nil {
		for _, r := range s.Manifest.For("windows") {
			names[r.ID] = r.Name
		}
	}
	for _, j := range list {
		if j.Type != jobs.TypeThirdParty || j.State != jobs.StateDone || j.FinishedAt == nil || !j.FinishedAt.After(since) {
			continue
		}
		full, found, err := s.Store.JobForMachine(ctx, machineID, j.ID)
		if err != nil {
			return nil, err
		}
		var payload jobs.Job
		if !found || json.Unmarshal([]byte(full.Payload), &payload) != nil {
			continue
		}
		p, err := jobs.ParseThirdPartyParams(payload.Params)
		if err != nil {
			continue
		}
		name := names[p.App]
		if name == "" {
			name = p.App
		}
		name += " " + p.Version
		if seen[name] {
			continue
		}
		seen[name] = true
		d := *j.FinishedAt
		out = append(out, protocol.SelfServiceRebootUpdate{Name: name, Date: &d, Source: "patchd"})
	}
	slices.SortStableFunc(out, func(a, b protocol.SelfServiceRebootUpdate) int { return b.Date.Compare(*a.Date) })
	if len(out) > protocol.MaxRebootUpdates {
		out = out[:protocol.MaxRebootUpdates]
	}
	return out, nil
}

// rebootAuthor: quem pediu o reinício, dito para o usuário (sem o nome de quem é da TI).
func rebootAuthor(createdBy, account string) string {
	switch {
	case strings.EqualFold(createdBy, Author(account)):
		return "você"
	case strings.HasPrefix(createdBy, Author("")):
		return "outro usuário deste computador"
	case strings.HasPrefix(createdBy, "campanha "):
		return "a campanha de atualizações da equipe de TI"
	}
	return "a equipe de TI"
}

// reboot atende o "reiniciar" (Aula 17.6): agora (o aviso mínimo) ou na hora escolhida, até
// 12 h à frente. O servidor não exige o pendente da última busca (ela pode ter um dia): o
// agente confere na hora e, sem nada pendente, o job volta dispensado.
func (s *Service) reboot(ctx context.Context, machineID string, req protocol.SelfServiceRequest, o Outcome) (Outcome, error) {
	refuse := func(why, msg string) (Outcome, error) {
		o.Why = why
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceRefused, Message: msg}
		return o, nil
	}
	st, err := s.rebootState(ctx, machineID, req.Account)
	if err != nil {
		return o, err
	}
	switch {
	case st == nil:
		return refuse("sem inventário do Windows", "Este computador não pode ser reiniciado por aqui.")
	case !st.Allowed:
		return refuse("servidor", "Em servidores, o reinício fica com a equipe de TI.")
	case st.JobID > 0:
		o.Why = fmt.Sprintf("reinício %d em aberto", st.JobID)
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceWait, JobID: st.JobID,
			Message: "Já há um reinício agendado para este computador."}
		return o, nil
	case req.App != "":
		return refuse("reiniciar com programa", "Pedido inválido.")
	}
	now := s.Now()
	params, err := jobs.RebootParamsJSON(jobs.MinRebootDelay)
	ttl := rebootTTL
	o.Version = "agora"
	msg := "Pedido aceito: o computador reinicia em 2 minutos. Salve seu trabalho agora."
	if req.RebootAt != nil {
		d := req.RebootAt.Sub(now)
		if d < jobs.MinRebootDelay-time.Minute || d > jobs.MaxScheduledReboot {
			return refuse("hora fora do intervalo", "Escolha um horário entre daqui a 2 minutos e daqui a 12 horas.")
		}
		params, err = jobs.RebootAtParamsJSON(*req.RebootAt)
		ttl += d
		o.Version = req.RebootAt.UTC().Format(time.RFC3339)
		msg = "Pedido aceito: o computador reinicia no horário escolhido. O Windows avisa antes; salve seu trabalho até lá."
	}
	if err != nil {
		return o, err
	}
	rec, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: machineID, Type: jobs.TypeReboot, TTL: ttl,
		By: Author(req.Account), Params: params}, now)
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		return refuse("máquina aposentada", "Esta máquina não está mais no parque.")
	}
	if err != nil {
		return o, err
	}
	o.Job = &rec
	o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: rec.ID, Message: msg}
	return o, nil
}

// requestApproval guarda o pedido para a TI (Aula 18.3): nada é instalado agora. Um segundo
// pedido do mesmo programa, com o primeiro esperando, não cria outro.
func (s *Service) requestApproval(ctx context.Context, machineID string, req protocol.SelfServiceRequest, it Item, o Outcome) (Outcome, error) {
	qs, err := s.userRequests(ctx, machineID, req.Account)
	if err != nil {
		return o, err
	}
	for _, q := range qs {
		if q.AppID == it.AppID && q.State == store.RequestWaiting {
			o.Why = fmt.Sprintf("pedido %d já aguarda a TI", q.ID)
			o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceWait, RequestID: q.ID,
				Message: fmt.Sprintf("Seu pedido de %s já está com a TI (nº %d). Acompanhe em Meus pedidos.", it.Name, q.ID)}
			return o, nil
		}
	}
	id, err := s.Store.CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: machineID, Account: req.Account,
		Groups: req.Groups, AppID: it.AppID, Action: req.Action, Version: it.Target})
	if err != nil {
		return o, err
	}
	o.RequestID, o.Version = id, it.Target
	o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceApproval, RequestID: id,
		Message: fmt.Sprintf("Pedido enviado para a aprovação da TI (nº %d): %s %s. Assim que for aprovado, a instalação começa sozinha; acompanhe em Meus pedidos.",
			id, it.Name, it.Target)}
	return o, nil
}

// ErrNotFound: pedido inexistente.
var ErrNotFound = errors.New("pedido não encontrado")

// Approval é o que a decisão da TI produziu.
type Approval struct {
	Request store.SelfServiceRequest
	Job     *jobs.Record
	Name    string // o nome do programa
}

// Approve aprova o pedido (Aula 18.3): a loja é refeita com a conta e os grupos guardados no
// pedido — o programa tem de continuar oferecido a esse usuário, com a mesma ação — e o job
// sai como se o usuário tivesse pedido agora, com ele como autor. why: por que não deu (a
// mensagem para quem aprova); o pedido continua aguardando, para ser recusado com o motivo.
func (s *Service) Approve(ctx context.Context, id int64, by string) (Approval, string, error) {
	var a Approval
	if s.Key == nil {
		return a, "", ErrOff
	}
	if _, err := s.Store.ExpireSelfServiceRequests(ctx, s.Now().Add(-store.RequestTTL), s.Now()); err != nil {
		return a, "", err
	}
	q, found, err := s.Store.SelfServiceRequestByID(ctx, id)
	switch {
	case err != nil:
		return a, "", err
	case !found:
		return a, "", ErrNotFound
	case q.State != store.RequestWaiting:
		return a, "o pedido já foi decidido ou expirou", nil
	}
	a.Request = q
	items, err := s.items(ctx, q.MachineID, protocol.SelfServiceUser{Account: q.Account, Groups: q.Groups})
	if err != nil {
		return a, "", err
	}
	i := slices.IndexFunc(items, func(it Item) bool { return it.AppID == q.AppID })
	if i < 0 || items[i].Action != q.Action {
		return a, "o pedido não vale mais: o programa saiu da loja desse usuário, ou mudou na máquina (já instalado, já em dia). Recuse com o motivo.", nil
	}
	it := items[i]
	a.Name = it.Name
	if open, err := s.openJob(ctx, q.MachineID); err != nil {
		return a, "", err
	} else if open > 0 {
		return a, fmt.Sprintf("há uma instalação em andamento nessa máquina (job %d); aprove quando ela terminar", open), nil
	}
	p, why, err := s.params(ctx, q.MachineID, it, protocol.SelfServiceUser{Account: q.Account})
	if err != nil {
		return a, "", err
	}
	if why != "" {
		return a, "o programa está indisponível agora: " + why, nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return a, "", err
	}
	rec, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: q.MachineID, Type: jobs.TypeThirdParty, TTL: JobTTL,
		By: Author(q.Account), Params: raw}, s.Now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		return a, "a máquina não está mais no parque", nil
	}
	if err != nil {
		return a, "", err
	}
	if err := s.Store.DecideSelfServiceRequest(ctx, id, store.RequestApproved, by, "", rec.ID, s.Now()); err != nil {
		// Alguém decidiu no meio do caminho: o job não sai.
		_, _ = s.Store.CancelJob(ctx, rec.ID, by)
		if errors.Is(err, store.ErrRequestDecided) {
			return a, "o pedido já foi decidido ou expirou", nil
		}
		return a, "", err
	}
	a.Job = &rec
	return a, "", nil
}

// Refuse recusa o pedido, com o motivo (que o usuário vê em Meus pedidos).
func (s *Service) Refuse(ctx context.Context, id int64, by, note string) (store.SelfServiceRequest, string, error) {
	note = strings.TrimSpace(note)
	if r := []rune(note); len(r) > MaxNote {
		note = string(r[:MaxNote])
	}
	q, found, err := s.Store.SelfServiceRequestByID(ctx, id)
	switch {
	case err != nil:
		return q, "", err
	case !found:
		return q, "", ErrNotFound
	}
	if err := s.Store.DecideSelfServiceRequest(ctx, id, store.RequestRefused, by, note, 0, s.Now()); err != nil {
		if errors.Is(err, store.ErrRequestDecided) {
			return q, "o pedido já foi decidido ou expirou", nil
		}
		return q, "", err
	}
	q.State, q.Note = store.RequestRefused, note
	return q, "", nil
}

// canCloseForUser: o agente da máquina fecha o programa a pedido do usuário, e o job tem os
// processos do programa (sem eles, o agente nem confere se está aberto).
func (s *Service) canCloseForUser(ctx context.Context, machineID string, p jobs.ThirdPartyParams) (bool, error) {
	if len(p.Processes) == 0 || p.Replace != nil {
		return false, nil
	}
	caps, err := s.Store.MachineCapabilities(ctx, machineID)
	if err != nil {
		return false, err
	}
	return slices.Contains(caps, protocol.CapabilityCloseByUser), nil
}

// closeOffers marca, em Meus pedidos, o pedido mais recente de cada programa que ficou adiado
// por programa aberto e pode ser pedido de novo com "Fechar o programa e instalar": o programa
// continua na loja com a mesma ação, sem passar pela TI, nada em andamento na máquina, a regra
// lista os processos e o agente sabe fechar a pedido do usuário.
func (s *Service) closeOffers(ctx context.Context, machineID string, out *protocol.SelfServiceCatalog) error {
	if out.OpenJob != 0 || s.Manifest == nil {
		return nil
	}
	seen := map[string]bool{}
	var cand []int
	for i, j := range out.Requests {
		if j.ID == 0 || j.App == "" || j.App == "reinicio" || seen[j.App] {
			continue
		}
		seen[j.App] = true
		if j.State == jobs.StatePostponed && strings.Contains(j.Detail, jobs.ErrProgramOpen.Error()) {
			cand = append(cand, i)
		}
	}
	if len(cand) == 0 {
		return nil
	}
	caps, err := s.Store.MachineCapabilities(ctx, machineID)
	if err != nil || !slices.Contains(caps, protocol.CapabilityCloseByUser) {
		return err
	}
	rules := s.Manifest.For("windows")
	for _, i := range cand {
		j := &out.Requests[i]
		ri := slices.IndexFunc(rules, func(a thirdparty.App) bool { return a.ID == j.App })
		if ri < 0 || rules[ri].Install == nil || len(rules[ri].Install.Processes) == 0 {
			continue
		}
		ii := slices.IndexFunc(out.Items, func(it protocol.SelfServiceItem) bool { return it.App == j.App })
		if ii < 0 {
			continue
		}
		it := out.Items[ii]
		if (it.Action == ActionUpdate || it.Action == ActionInstall) && !it.Approval && it.Pending == 0 {
			j.CloseAction = it.Action
		}
	}
	return nil
}

// windowsJob: o job windows_update em aberto na máquina (de qualquer autor: a campanha, o
// painel ou outro usuário), ou 0.
func (s *Service) windowsJob(ctx context.Context, machineID string) (int64, error) {
	list, err := s.Store.Jobs(ctx, machineID, openJobsScan)
	if err != nil {
		return 0, err
	}
	for _, j := range list {
		if j.Type == jobs.TypeWinUpdate && !jobs.Final(j.State) && j.NotAfter.After(s.Now()) {
			return j.ID, nil
		}
	}
	return 0, nil
}

// windowsState: a seção Windows da Central (nil: desligado, sem Windows ou sem busca).
func (s *Service) windowsState(ctx context.Context, machineID string) (*protocol.SelfServiceWindowsState, error) {
	if s.Windows == nil {
		return nil, nil
	}
	plan, err := s.Windows(ctx, machineID)
	if err != nil || plan == nil {
		return nil, err
	}
	st := plan.State
	if st.JobID, err = s.windowsJob(ctx, machineID); err != nil {
		return nil, err
	}
	if len(st.Updates) > protocol.MaxSelfServiceWinUpdates {
		st.Updates = st.Updates[:protocol.MaxSelfServiceWinUpdates]
	}
	return &st, nil
}

// windows atende o "instalar as atualizações do Windows": o mesmo job do botão do painel, com
// o que o servidor decide na hora (o app só pede). Sem reinício: o reinício continua sendo o
// da aba Reinício.
func (s *Service) windows(ctx context.Context, machineID string, req protocol.SelfServiceRequest, o Outcome) (Outcome, error) {
	refuse := func(why, msg string) (Outcome, error) {
		o.Why = why
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceRefused, Message: msg}
		return o, nil
	}
	if req.App != "" || req.RebootAt != nil || req.Close {
		return refuse("windows com programa, hora ou fechar", "Pedido inválido.")
	}
	if s.Windows == nil {
		return refuse("windows desligado", "As atualizações do Windows não estão disponíveis por aqui.")
	}
	plan, err := s.Windows(ctx, machineID)
	if err != nil {
		return o, err
	}
	if plan == nil {
		return refuse("windows desligado ou sem busca", "As atualizações do Windows não estão disponíveis por aqui.")
	}
	open, err := s.windowsJob(ctx, machineID)
	if err != nil {
		return o, err
	}
	switch {
	case open > 0:
		o.Why = fmt.Sprintf("windows_update %d em aberto", open)
		o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceWait, JobID: open,
			Message: "Já há uma instalação de atualizações do Windows em andamento neste computador."}
		return o, nil
	case plan.State.Problem != "":
		return refuse("busca do windows falhou", "A busca de atualizações deste computador falhou. A equipe de TI foi avisada pelo painel; tente mais tarde.")
	case len(plan.Params.Updates) == 0:
		return refuse("nada aprovado e pendente", "Não há atualização do Windows liberada pela TI para instalar agora.")
	}
	if err := plan.Params.Validate(); err != nil {
		return refuse("windows: "+err.Error(), "Pedido inválido.")
	}
	raw, err := json.Marshal(plan.Params)
	if err != nil {
		return o, err
	}
	rec, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: machineID, Type: jobs.TypeWinUpdate, TTL: JobTTL,
		By: Author(req.Account), Params: raw}, s.Now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		return refuse("máquina aposentada", "Esta máquina não está mais no parque.")
	}
	if err != nil {
		return o, err
	}
	o.Job, o.Version = &rec, strings.Join(plan.Params.Labels(), ", ")
	o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: rec.ID,
		Message: fmt.Sprintf("Pedido aceito: %d atualização(ões) do Windows. A instalação roda em segundo plano e pode demorar; você pode continuar trabalhando. O computador não reinicia sozinho: se precisar, a Central avisa.", len(plan.Params.Updates))}
	return o, nil
}
