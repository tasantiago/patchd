// Package selfservice decide o que o usuário de uma máquina pode pedir sozinho no
// autoatendimento (Módulo 17): a "loja" com os programas homologados. A decisão junta a
// oferta do admin (Aula 17.1: o programa, atualizar e/ou instalar, para quais grupos do AD),
// a aprovação da versão (14.5) e o plano da máquina (14.3). O usuário nunca escolhe versão,
// arquivo ou argumento: só o programa, entre os que aparecem aqui.
package selfservice

import (
	"cmp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
	"github.com/tasantiago/patchd/internal/version"
)

// Ações de um item.
const (
	ActionUpdate    = "atualizar"    // instalado, com versão aprovada mais nova
	ActionInstall   = "instalar"     // não instalado, e a oferta libera instalar
	ActionCurrent   = "em_dia"       // instalado na versão aprovada mais nova (ou acima)
	ActionInstalled = "instalado"    // instalado; a oferta não libera atualizar
	ActionBlocked   = "indisponivel" // liberado, mas o patchd não pode fazer nesta máquina
)

var actionOrder = []string{ActionUpdate, ActionInstall, ActionBlocked, ActionCurrent, ActionInstalled}

// Item é um programa como o usuário vê na máquina.
type Item struct {
	AppID, Name string
	Installed   string // versão instalada (vazia: não instalado)
	Target      string // a versão que o pedido levaria
	Action      string
	Reason      string // indisponível: por quê
	// NeedsApproval (Aula 18.3): a oferta exige que a TI aprove o pedido.
	NeedsApproval bool
	// Replaces: a atualização troca a instalação (tpinstall.Decision.Regularize).
	Replaces bool
	// Category: a categoria na loja (protocol.StoreCategories).
	Category string
}

// StoreCategory: a categoria da oferta na loja, ou a padrão do programa.
func StoreCategory(o store.SelfServiceOffer) string {
	if o.Category != "" {
		return o.Category
	}
	return thirdparty.DefaultStoreCategory(o.AppID)
}

// Machine é o que o catálogo usa da máquina.
type Machine struct {
	Software []protocol.Software
	Arch     string // tpinstall.MachineArch
	// Options: o que o plano faz além da atualização do mesmo tipo (a regularização).
	Options tpinstall.Options
}

// Catalog monta a loja de um usuário numa máquina. member diz se o usuário está no grupo
// (Aula 17.2: o AD, com grupos aninhados; na prévia do painel, a lista digitada).
func Catalog(offers []store.SelfServiceOffer, member func(group string) bool, m Machine, manifest thirdparty.Manifest,
	pkgs []store.ThirdPartyPackage, approvals []store.ThirdPartyApproval) []Item {
	ok := map[string]bool{}
	for _, a := range approvals {
		ok[store.TPApprovalKey(a.AppID, a.Version)] = true
	}
	// Só o que está aprovado: a oferta libera o programa, nunca uma versão.
	var approved []store.ThirdPartyPackage
	for _, p := range pkgs {
		if ok[store.TPApprovalKey(p.AppID, p.Version)] {
			approved = append(approved, p)
		}
	}
	plan := tpinstall.PlanWith(m.Software, manifest, approved, m.Arch, m.Options)
	rules := manifest.For("windows")

	var out []Item
	for _, o := range offers {
		i := slices.IndexFunc(rules, func(a thirdparty.App) bool { return a.ID == o.AppID })
		if i < 0 || rules[i].Install == nil || !allowed(o, member) {
			continue
		}
		rule := rules[i]
		it := Item{AppID: rule.ID, Name: rule.Name, NeedsApproval: o.NeedsApproval, Category: StoreCategory(o)}
		if j := slices.IndexFunc(plan, func(d tpinstall.Decision) bool { return d.App.ID == rule.ID }); j >= 0 {
			d := plan[j]
			it.Installed = d.Installed
			switch {
			case !o.CanUpdate:
				it.Action = ActionInstalled
			case d.State == tpinstall.StateUpdate:
				it.Action, it.Target, it.Replaces = ActionUpdate, d.Package.Version, d.Regularize != nil
			case d.State == tpinstall.StateCurrent:
				it.Action = ActionCurrent
			case d.State == tpinstall.StateNoPackage:
				it.Action, it.Reason = ActionBlocked, "nenhuma versão aprovada para esta instalação"
			default:
				it.Action, it.Reason = ActionBlocked, "fale com o suporte ("+strings.ToLower(tpinstall.Labels[d.State])+": "+d.Reason+")"
			}
			if it.Installed == "" && len(d.Entries) > 0 {
				it.Installed = d.Entries[0].Software.Version
			}
		} else {
			if !o.CanInstall {
				continue // não instalado e a oferta só atualiza: nada a mostrar
			}
			if p := tpinstall.ForNewInstall(rule, approved, m.Arch); p != nil {
				it.Action, it.Target = ActionInstall, p.Version
			} else {
				it.Action, it.Reason = ActionBlocked, noInstallerReason(rule.ID, approved)
			}
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		return cmp.Or(cmp.Compare(slices.Index(actionOrder, a.Action), slices.Index(actionOrder, b.Action)), strings.Compare(a.Name, b.Name))
	})
	return out
}

// noInstallerReason: por que a instalação nova não sai. Com versão aprovada, o que falta é o
// instalador do tipo da regra (ou da arquitetura da máquina) no servidor, e não a aprovação.
func noInstallerReason(app string, approved []store.ThirdPartyPackage) string {
	var newest string
	for _, p := range approved {
		if p.AppID == app && (newest == "" || newerVersion(p.Version, newest)) {
			newest = p.Version
		}
	}
	if newest == "" {
		return "nenhuma versão aprovada para esta máquina"
	}
	return "a versão aprovada (" + newest + ") ainda não tem o instalador para esta máquina no servidor: fale com a TI"
}

// newerVersion: x é mais nova que y (pela versão normalizada; sem entender, pelo texto).
func newerVersion(x, y string) bool {
	nx, _ := thirdparty.Normalize(x)
	ny, _ := thirdparty.Normalize(y)
	if c, err := version.CompareDotted(nx, ny); err == nil && c != 0 {
		return c > 0
	}
	return x > y
}

// allowed: oferta sem grupo vale para qualquer usuário; com grupos, basta um.
func allowed(o store.SelfServiceOffer, member func(string) bool) bool {
	if len(o.Groups) == 0 {
		return true
	}
	return slices.ContainsFunc(o.Groups, member)
}

// ListMember: o teste de grupo da prévia, pela lista digitada (sem diferenciar maiúsculas).
func ListMember(groups []string) func(string) bool {
	return func(g string) bool {
		return slices.ContainsFunc(groups, func(x string) bool { return strings.EqualFold(strings.TrimSpace(x), g) })
	}
}
