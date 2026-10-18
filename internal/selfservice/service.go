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
}

// Service atende a loja e os pedidos (Aula 17.2). A API do agente e a linha de comando usam
// o mesmo serviço: o mesmo pedido dá a mesma decisão.
type Service struct {
	Store       Store
	Manifest    *thirdparty.Manifest
	Key         ed25519.PrivateKey
	PackagesDir string // PATCHD_CONTENT_DIR/pacotes
	Now         func() time.Time
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
			Action: it.Action, Reason: it.Reason, Approval: it.NeedsApproval}
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
	out.Reboot, err = s.rebootState(ctx, machineID)
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
		if (j.Type != jobs.TypeThirdParty && j.Type != jobs.TypeReboot) || !strings.EqualFold(j.CreatedBy, Author(account)) {
			continue
		}
		if j.Type == jobs.TypeReboot { // Aula 17.6
			out = append(out, protocol.SelfServiceJob{ID: j.ID, App: "reinicio", Name: RebootName, State: j.State,
				CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Detail: short(j.Detail, 200)})
			continue
		}
		full, found, err := s.Store.JobForMachine(ctx, machineID, j.ID)
		if err != nil {
			return nil, err
		}
		item := protocol.SelfServiceJob{ID: j.ID, State: j.State, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Detail: short(j.Detail, 200)}
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
	return Catalog(offers, TokenMember(u.Groups), Machine{Software: inv.Software, Arch: arch}, *s.Manifest, pkgs, approvals), nil
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
	p, why, err := s.params(ctx, machineID, it)
	if err != nil {
		return o, err
	}
	if why != "" {
		return refuse(why, "Este programa está indisponível agora. Avise o suporte.")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return o, err
	}
	rec, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: machineID, Type: jobs.TypeThirdParty, TTL: JobTTL,
		By: Author(req.Account), Params: raw}, s.Now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		return refuse("máquina aposentada", "Esta máquina não está mais na frota.")
	}
	if err != nil {
		return o, err
	}
	o.Job, o.Version = &rec, p.Version
	verb := "instalação"
	if req.Action == protocol.SelfServiceUpdate {
		verb = "atualização"
	}
	o.Response = protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: rec.ID,
		Message: fmt.Sprintf("Pedido aceito: %s de %s %s. Feche o programa, se estiver aberto; o patchd avisa quando terminar.", verb, it.Name, p.Version)}
	return o, nil
}

// params monta o job pelo mesmo caminho do painel: a decisão da máquina com os instaladores
// aprovados, conferidos no cache.
func (s *Service) params(ctx context.Context, machineID string, it Item) (jobs.ThirdPartyParams, string, error) {
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
	if it.Action == ActionInstall {
		d.Package = tpinstall.ForNewInstall(rule, versionPkgs, arch)
	} else {
		for _, x := range tpinstall.Plan(inv.Software, *s.Manifest, versionPkgs, arch) {
			if x.App.ID == it.AppID && x.State == tpinstall.StateUpdate {
				d.Package = x.Package
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
func (s *Service) rebootState(ctx context.Context, machineID string) (*protocol.SelfServiceRebootState, error) {
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
	return st, nil
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
	st, err := s.rebootState(ctx, machineID)
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
		return refuse("máquina aposentada", "Esta máquina não está mais na frota.")
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
	p, why, err := s.params(ctx, q.MachineID, it)
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
		return a, "a máquina não está mais na frota", nil
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
