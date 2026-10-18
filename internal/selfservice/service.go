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
	for _, it := range items {
		out.Items = append(out.Items, protocol.SelfServiceItem{App: it.AppID, Name: it.Name, Installed: it.Installed, Target: it.Target,
			Action: it.Action, Reason: it.Reason})
	}
	if out.OpenJob, err = s.openJob(ctx, machineID); err != nil {
		return out, err
	}
	out.Requests, err = s.requests(ctx, machineID, u.Account)
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
		if j.Type != jobs.TypeThirdParty || !strings.EqualFold(j.CreatedBy, Author(account)) {
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
	return out, nil
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
	Version  string
	Seen     bool   // o usuário aparece entre os conectados da última coleta (15.6)
	Why      string // recusado: o motivo para o log (a mensagem do usuário é mais curta)
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
