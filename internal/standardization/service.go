// Package standardization aplica padrões aditivos de aplicativos e atalhos por grupo.
package standardization

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const RetryAfter = 6 * time.Hour
const maxBatch = 5

type Store interface {
	jobs.Queue
	StandardizationPolicies(context.Context) ([]store.StandardizationPolicy, error)
	WithStandardizationLock(context.Context, string, func(context.Context) error) (bool, error)
	StandardizationJobs(context.Context, string, time.Time) (map[string]store.JobRow, int, error)
	SaveStandardizationState(context.Context, store.StandardizationState) error
	LatestInventory(context.Context, string) (protocol.InventoryReport, bool, error)
	ThirdPartyPackages(context.Context) ([]store.ThirdPartyPackage, error)
	ThirdPartyApprovals(context.Context) ([]store.ThirdPartyApproval, error)
}

type Service struct {
	Store       Store
	Manifest    *thirdparty.Manifest
	Key         ed25519.PrivateKey
	PackagesDir string
	BundlesDir string
	Now         func() time.Time
	OnCreated   func(jobs.Record, string, string)
}

// GroupMember respeita o domínio quando ele é explicitado no grupo da política.
func GroupMember(want string, groups []string) bool {
	for _, have := range groups {
		if strings.Contains(want, `\`) {
			if strings.EqualFold(want, have) {
				return true
			}
			continue
		}
		if i := strings.LastIndex(have, `\`); i >= 0 {
			have = have[i+1:]
		}
		if strings.EqualFold(want, have) {
			return true
		}
	}
	return false
}

func Matches(p store.StandardizationPolicy, machine string, user protocol.SelfServiceUser) bool {
	return p.Enabled && (p.AllMachines || slices.Contains(p.Machines, machine) || slices.ContainsFunc(p.ADGroups, func(g string) bool { return GroupMember(g, user.Groups) }))
}

// Resolve faz a união. Nomes de atalhos conflitantes são bloqueados, sem escolher um destino arbitrário.
func Resolve(policies []store.StandardizationPolicy, machine string, user protocol.SelfServiceUser, platform ...string) (protocol.StandardizationPlan, []string) {
	out := protocol.StandardizationPlan{Policies: []string{}, Apps: []protocol.StandardAppState{}, Shortcuts: []protocol.StandardShortcut{}}
	apps := map[string]bool{}
	links := map[string]protocol.StandardShortcut{}
	conflicts := map[string]bool{}
	matched := []store.StandardizationPolicy{}
	for _, p := range policies {
		if !Matches(p, machine, user) {
			continue
		}
		matched = append(matched, p)
		out.Policies = append(out.Policies, p.Name)
		if len(platform)==0 || platform[0]=="windows" {for _, app := range p.Apps { apps[app] = true }}
		for _, link := range p.Shortcuts {
			if len(platform)>0 && !link.SupportsOS(platform[0]) {continue}
			key := strings.ToLower(link.Name)
			if old, ok := links[key]; ok && (old.Kind != link.Kind || old.Target != link.Target || old.Icon != link.Icon) {
				conflicts[key] = true
			}
			links[key] = link
		}
	}
	keys := []string{}
	for key := range links {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if conflicts[key] {
			out.Warnings = append(out.Warnings, "Atalho com destinos conflitantes: "+links[key].Name)
			continue
		}
		if len(out.Shortcuts) == protocol.MaxStandardizationShortcuts {
			out.Warnings = append(out.Warnings, "Limite de 24 atalhos resultantes atingido; ajuste os grupos.")
			break
		}
		out.Shortcuts = append(out.Shortcuts, links[key])
	}
	ids := []string{}
	for id := range apps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) > protocol.MaxStandardizationApps {
		ids = ids[:protocol.MaxStandardizationApps]
		out.Warnings = append(out.Warnings, "Limite de 64 aplicativos resultantes atingido; ajuste os grupos.")
	}
	if len(out.Policies) > 100 {
		out.Policies = out.Policies[:100]
		out.Warnings = append(out.Warnings, "Mais de 100 grupos aplicáveis; confira a configuração.")
	}
	b, _ := json.Marshal(struct {
		Policies []store.StandardizationPolicy
		User     string
	}{matched, strings.ToLower(user.Account)})
	h := sha256.Sum256(b)
	out.Revision = hex.EncodeToString(h[:])
	return out, ids
}

func (s *Service) Reconcile(ctx context.Context, machine string, user protocol.SelfServiceUser) (protocol.StandardizationPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	policies, err := s.Store.StandardizationPolicies(ctx)
	if err != nil {
		return protocol.StandardizationPlan{}, err
	}
	inv,known,err:=s.Store.LatestInventory(ctx,machine);if err!=nil{return protocol.StandardizationPlan{},err}
 platform:=inv.OS.Family;if platform==""&&inv.OS.ID=="windows"{platform="windows"}
 out, apps := Resolve(policies, machine, user, platform)
 installers:=resolveInstallers(policies,machine,user,platform,&out)
 if len(installers)>protocol.MaxStandardizationApps-len(apps){installers=installers[:protocol.MaxStandardizationApps-len(apps)];out.Warnings=append(out.Warnings,"Limite de 64 aplicativos resultantes atingido.")}
 if !known {out.Warnings=append(out.Warnings,"Aguardando inventário para identificar o sistema e aplicar os aplicativos e atalhos.")}
	out.GeneratedAt = now
	locked, err := s.Store.WithStandardizationLock(ctx, machine, func(ctx context.Context) error {
		if len(apps) > 0 {
			states, n, err := s.applications(ctx, machine, apps, now)
			if err != nil {
				return err
			}
			out.Apps, out.CreatedJobs = states, n
		}
        if len(installers)>0 {
          states,n,err:=s.scriptApplications(ctx,machine,installers,inv,now,maxBatch-out.CreatedJobs);if err!=nil{return err}
          out.Apps=append(out.Apps,states...);out.CreatedJobs+=n
        }
		return s.Store.SaveStandardizationState(ctx, store.StandardizationState{MachineID: machine, Account: user.Account, Plan: out, EvaluatedAt: now})
	})
	if err != nil {
		return out, err
	}
	if !locked {
		out.Warnings = append(out.Warnings, "Outra avaliação deste computador está em andamento; a aplicação de aplicativos será retomada na próxima consulta.")
	}
	return out, nil
}

func (s *Service) applications(ctx context.Context, machine string, apps []string, now time.Time) ([]protocol.StandardAppState, int, error) {
	out := []protocol.StandardAppState{}
	for _, id := range apps {
		out = append(out, protocol.StandardAppState{App: id, Name: id, State: "bloqueado"})
	}
	block := func(detail string) ([]protocol.StandardAppState, int, error) {
		for i := range out {
			out[i].Detail = detail
		}
		return out, 0, nil
	}
	inv, found, err := s.Store.LatestInventory(ctx, machine)
	if err != nil {
		return nil, 0, err
	}
	if !found {
		return block("Aguardando o primeiro inventário.")
	}
	arch, ok := tpinstall.MachineArch(inv.OS.Arch)
	if inv.OS.ID != "windows" || !ok {
		return block("Instalação automática de padrões disponível para Windows com arquitetura suportada.")
	}
	if s.Manifest == nil {
		return block("Catálogo de aplicativos não configurado no servidor.")
	}
	pkgs, err := s.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, 0, err
	}
	approvals, err := s.Store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, 0, err
	}
	approved := map[string]bool{}
	for _, a := range approvals {
		approved[store.TPApprovalKey(a.AppID, a.Version)] = true
	}
	available := []store.ThirdPartyPackage{}
	for _, p := range pkgs {
		if approved[store.TPApprovalKey(p.AppID, p.Version)] {
			available = append(available, p)
		}
	}
	decisions := tpinstall.Plan(inv.Software, *s.Manifest, available, arch)
	latest, open, err := s.Store.StandardizationJobs(ctx, machine, now)
	if err != nil {
		return nil, 0, err
	}
	created := 0
	for i, id := range apps {
		state := &out[i]
		ri := slices.IndexFunc(s.Manifest.For("windows"), func(a thirdparty.App) bool { return a.ID == id && a.Install != nil })
		if ri < 0 {
			state.Detail = "Aplicativo fora do catálogo de instalação."
			continue
		}
		rule := s.Manifest.For("windows")[ri]
		state.Name = rule.Name
		var decision tpinstall.Decision
		if di := slices.IndexFunc(decisions, func(d tpinstall.Decision) bool { return d.App.ID == id }); di >= 0 {
			decision = decisions[di]
			if decision.Package != nil {
				state.Target = decision.Package.Version
			}
			if decision.State == tpinstall.StateCurrent {
				state.State = "em_dia"
				state.Detail = "Versão confirmada no inventário."
				continue
			}
			if decision.State != tpinstall.StateUpdate {
				state.Detail = tpinstall.Labels[decision.State] + ": " + decision.Reason
				continue
			}
		} else {
			decision = tpinstall.Decision{App: rule, State: tpinstall.StateUpdate, Package: tpinstall.ForNewInstall(rule, available, arch)}
		}
		if decision.Package == nil {
			state.Detail = "Nenhum instalador aprovado compatível com este computador."
			continue
		}
		state.Target = decision.Package.Version
		if last, ok := latest[id]; ok {
			if !jobs.Final(last.State) {
				state.State = "em_andamento"
				state.JobID = last.ID
				state.Detail = "Job aguardando execução ou resultado."
				continue
			}
			if last.FinishedAt != nil && now.Sub(*last.FinishedAt) < RetryAfter && last.State != jobs.StateDone && last.State != jobs.StateCanceled {
				state.State = "falhou"
				state.JobID = last.ID
				state.Detail = "Última tentativa: " + last.State + ". Nova tentativa após seis horas."
				continue
			}
		}
		if open > 0 || created >= maxBatch {
			state.State = "aguardando"
			state.Detail = "Aguardando a fila de instalação do computador."
			continue
		}
		if len(s.Key) != ed25519.PrivateKeySize || s.PackagesDir == "" {
			state.Detail = "Chave de jobs ou repositório de instaladores não configurado."
			continue
		}
		if _, err := pkgcache.Check(s.PackagesDir, decision.Package.SHA256, decision.Package.MSI()); err != nil {
			state.Detail = "Instalador ausente ou diferente no cache do servidor."
			continue
		}
		params, err := tpinstall.JobParams(decision)
		if err != nil {
			state.Detail = "Parâmetros do instalador incompatíveis com um job seguro."
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, created, err
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, created, err
		}
		record, err := jobs.Issue(ctx, s.Store, s.Key, jobs.Request{MachineID: machine, Type: jobs.TypeThirdParty, TTL: 24 * time.Hour, By: store.StandardizationAuthor, Params: raw}, now)
		if err != nil {
			return nil, created, fmt.Errorf("agendar padrão: %w", err)
		}
		if s.OnCreated != nil {
			s.OnCreated(record, id, state.Target)
		}
		created++
		state.State = "agendado"
		state.JobID = record.ID
		state.Detail = "Instalação assinada criada; o inventário posterior confirmará o resultado."
	}
	return out, created, nil
}
