// Package tpinstall decide, máquina a máquina, o que o patchd pode fazer com cada programa
// de terceiros que tem "install" na regra (Aula 14.3): atualizar com qual instalador
// guardado, ou por que não.
//
// O tipo do instalador vem da máquina, não da regra: o que está instalado por MSI (o
// inventário traz o product_code) só é atualizado por MSI; o que foi instalado por um exe,
// só por exe. Instalar o outro tipo deixa duas entradas no Desinstalar. Duas instalações
// visíveis do mesmo programa e instalações por bundle (WiX Burn) ficam fora da automação.
//
// A decisão usa só o inventário e os instaladores que o servidor já guardou (Aula 14.2):
// nada de rede por máquina.
package tpinstall

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
	"github.com/tasantiago/patchd/internal/winget"
)

// Estados da decisão.
const (
	StateUpdate     = "atualizar"
	StateCurrent    = "em_dia"
	StateNoPackage  = "sem_pacote"      // falta guardar um instalador do tipo da máquina
	StateDuplicate  = "duplicado"       // duas instalações visíveis: limpar antes
	StateBundle     = "bundle"          // instalado por bundle do Burn: o winget não tem o mesmo
	StateUserOnly   = "usuario"         // só instalações por usuário: o agente instala para a máquina
	StateBadVersion = "versao_ilegivel" // a versão instalada não se compara
)

// States: a ordem de exibição (o que pede ação primeiro).
var States = []string{StateUpdate, StateNoPackage, StateDuplicate, StateBundle, StateUserOnly, StateBadVersion, StateCurrent}

// Tipos de instalação, pelo que o inventário diz da entrada.
const (
	KindMSI    = "msi"
	KindBundle = "bundle"
	KindExe    = "exe"
)

// Kind classifica a entrada: MSI (product_code), bundle (bundle_upgrade_code) ou exe.
func Kind(s protocol.Software) string {
	switch {
	case s.ProductCode != "":
		return KindMSI
	case s.BundleUpgradeCode != "":
		return KindBundle
	}
	return KindExe
}

// families: os tipos de instalador do winget que atualizam cada tipo de instalação.
var families = map[string][]string{
	KindMSI: {"msi", "wix"},
	KindExe: {"nullsoft", "inno", "exe"},
}

// Family (Aula 23.5): os tipos de instalador do winget que atualizam o tipo de instalação
// (msi ou exe); nil para os outros. Uma cópia: quem chama pode mexer.
func Family(kind string) []string { return slices.Clone(families[kind]) }

// KindLabel: o nome do tipo de instalação para a tela (MSI, EXE).
func KindLabel(kind string) string { return strings.ToUpper(kind) }

// MachineArch converte a arquitetura do agente (GOARCH) para os nomes do winget.
func MachineArch(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "x64", true
	case "arm64":
		return "arm64", true
	case "386":
		return "x86", true
	}
	return "", false
}

// Entry é uma instalação do programa na máquina.
type Entry struct {
	Software  protocol.Software
	Kind      string
	Installed string // versão normalizada; vazia quando não se compara
}

// Decision é o que fazer com um programa numa máquina.
type Decision struct {
	App       thirdparty.App
	Entries   []Entry // instalações para a máquina, visíveis
	User      []Entry // instalações por usuário (fora da automação)
	State     string
	Reason    string
	Package   *store.ThirdPartyPackage // atualizar: o instalador; em dia: o mais novo guardado
	Installed string
}

// Plan decide cada programa com "install" encontrado no inventário. arch é a da máquina
// (MachineArch). Programas que a máquina não tem não aparecem.
func Plan(sw []protocol.Software, m thirdparty.Manifest, pkgs []store.ThirdPartyPackage, arch string) []Decision {
	groups := group(sw, m)
	out := make([]Decision, 0, len(groups))
	for _, d := range groups {
		decide(d, pkgs, arch)
		out = append(out, *d)
	}
	slices.SortStableFunc(out, func(a, b Decision) int {
		return cmp.Or(cmp.Compare(slices.Index(States, a.State), slices.Index(States, b.State)), strings.Compare(a.App.Name, b.App.Name))
	})
	return out
}

// group reconhece as instalações de cada programa com "install", na ordem do inventário.
// Entradas SystemComponent (o MSI escondido de um bundle) e atualizações não contam.
func group(sw []protocol.Software, m thirdparty.Manifest) []*Decision {
	rules := m.For("windows")
	byApp := map[string]*Decision{}
	var out []*Decision
	for _, s := range sw {
		if s.SystemComponent || s.IsUpdate {
			continue
		}
		i := slices.IndexFunc(rules, func(a thirdparty.App) bool { return a.Matches(s.Name, s.Publisher) })
		if i < 0 || rules[i].Install == nil {
			continue
		}
		a := rules[i]
		d := byApp[a.ID]
		if d == nil {
			d = &Decision{App: a}
			byApp[a.ID] = d
			out = append(out, d)
		}
		e := Entry{Software: s, Kind: Kind(s)}
		if v, ok := a.InstalledVersion(s.Name, s.Version); ok {
			e.Installed = v
		}
		if s.Scope == "user" {
			d.User = append(d.User, e)
		} else {
			d.Entries = append(d.Entries, e)
		}
	}
	return out
}

// Entries devolve as instalações de um programa: as de máquina (visíveis) e as de usuário.
// O servidor confere o job thirdparty por elas (Aula 14.4), com o mesmo reconhecimento do plano.
func Entries(sw []protocol.Software, m thirdparty.Manifest, app string) (machine, user []Entry) {
	for _, d := range group(sw, m) {
		if d.App.ID == app {
			return d.Entries, d.User
		}
	}
	return nil, nil
}

func decide(d *Decision, pkgs []store.ThirdPartyPackage, arch string) {
	switch {
	case len(d.Entries) == 0:
		d.State = StateUserOnly
		d.Reason = fmt.Sprintf("%d instalação(ões) por usuário: o agente instala para a máquina", len(d.User))
		return
	case len(d.Entries) > 1:
		var parts []string
		for _, e := range d.Entries {
			parts = append(parts, e.Kind+" "+cmp.Or(e.Software.Version, "?"))
		}
		d.State = StateDuplicate
		d.Reason = fmt.Sprintf("%d instalações visíveis (%s): limpar antes, deixando uma", len(d.Entries), strings.Join(parts, ", "))
		return
	}
	e := d.Entries[0]
	d.Installed = e.Installed
	if e.Kind == KindBundle {
		d.State = StateBundle
		d.Reason = "instalado por um bundle (WiX Burn): o MSI direto deixaria a entrada do bundle para trás"
		return
	}
	if e.Installed == "" {
		d.State = StateBadVersion
		d.Reason = fmt.Sprintf("versão instalada %q não se compara", e.Software.Version)
		return
	}
	best := newest(pkgs, d.App.ID, families[e.Kind], arch)
	if best == nil {
		// Sem -type, o fetch leva o primeiro que serve no manifesto, que pode ser do outro
		// tipo: a dica só dispensa o -type quando o da regra já é da família da máquina.
		// Aula 23.5: o painel baixa também do outro tipo (Baixar, com o tipo); a linha de
		// comando continua valendo.
		hint := "package fetch -app " + d.App.ID
		panel := "Baixar na página do programa"
		if !slices.Contains(families[e.Kind], d.App.Install.Type) {
			hint += fmt.Sprintf(" -type %s (o que o manifesto tiver: package show -app %s)",
				strings.Join(families[e.Kind], " ou "), d.App.ID)
			panel += " com o tipo " + KindLabel(e.Kind)
		}
		d.State = StateNoPackage
		d.Reason = fmt.Sprintf("nenhum instalador %s guardado para %s; use %s, ou %s", e.Kind, arch, panel, hint)
		return
	}
	d.Package = best
	pv, _ := thirdparty.Normalize(best.Version)
	if c, err := version.CompareDotted(e.Installed, pv); err == nil && c >= 0 {
		d.State = StateCurrent
		d.Reason = fmt.Sprintf("%s instalado; o mais novo guardado é %s", e.Installed, best.Version)
		return
	}
	d.State = StateUpdate
	d.Reason = fmt.Sprintf("%s → %s · %s · %s", e.Installed, best.Version, best.Type, best.Architecture)
}

// newest: o instalador guardado mais novo do programa, de um dos tipos e de uma arquitetura
// que roda na máquina; no empate de versão, a arquitetura preferida.
func newest(pkgs []store.ThirdPartyPackage, app string, types []string, arch string) *store.ThirdPartyPackage {
	var best *store.ThirdPartyPackage
	for i := range pkgs {
		p := &pkgs[i]
		if p.AppID != app || !slices.Contains(types, p.Type) || winget.ArchRank(arch, p.Architecture) < 0 {
			continue
		}
		v, ok := thirdparty.Normalize(p.Version)
		if !ok {
			continue
		}
		if best == nil {
			best = p
			continue
		}
		bv, _ := thirdparty.Normalize(best.Version)
		c, err := version.CompareDotted(v, bv)
		if err == nil && (c > 0 || c == 0 && winget.ArchRank(arch, p.Architecture) < winget.ArchRank(arch, best.Architecture)) {
			best = p
		}
	}
	return best
}

// Counts: quantas decisões em cada estado.
func Counts(ds []Decision) map[string]int {
	out := map[string]int{}
	for _, d := range ds {
		out[d.State]++
	}
	return out
}

// ForNewInstall (Aula 17.1): o instalador para uma instalação nova, numa máquina que não tem
// o programa. Sem instalação para seguir, vale o tipo da regra (o 7-Zip exige exe: instalar o
// MSI criaria outro padrão na frota); regra sem tipo, qualquer família.
func ForNewInstall(app thirdparty.App, pkgs []store.ThirdPartyPackage, arch string) *store.ThirdPartyPackage {
	types := append(slices.Clone(families[KindExe]), families[KindMSI]...)
	if app.Install != nil && app.Install.Type != "" {
		types = []string{app.Install.Type}
	}
	return newest(pkgs, app.ID, types, arch)
}
