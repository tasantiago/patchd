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
	KindApp    = "app" // macOS
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
	KindMSI:    {"msi", "wix"},
	KindExe:    {"nullsoft", "inno", "exe"},
	KindBundle: {"burn"}, // só quando a regra pede burn: o bundle novo atualiza o antigo
}

// bundleArch: a arquitetura de um bundle pelo nome ("Python 3.14.4 (64-bit)"). Um bundle de
// 32 bits e um de 64 são produtos diferentes (outro UpgradeCode): o de 64 instalado por cima
// de um de 32 deixaria os dois. exact: o nome diz qual, e só ela serve.
func bundleArch(name, machine string) (arch string, exact bool) {
	switch {
	case strings.Contains(name, "(32-bit)"):
		return "x86", true
	case strings.Contains(name, "(64-bit)"):
		return "x64", true
	case strings.Contains(name, "(ARM64)"):
		return "arm64", true
	}
	return machine, false
}

// Family (Aula 23.5): os tipos de instalador do winget que atualizam o tipo de instalação
// (msi ou exe); nil para os outros. Uma cópia: quem chama pode mexer.
func Family(kind string) []string { return slices.Clone(families[kind]) }

// FamilyOf: os tipos da mesma família do tipo (msi: msi e wix; nullsoft: nullsoft, inno e
// exe); burn só serve a burn.
func FamilyOf(t string) []string {
	for _, k := range []string{KindMSI, KindExe} {
		if slices.Contains(families[k], t) {
			return slices.Clone(families[k])
		}
	}
	return []string{t}
}

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
	// Target (macOS): a versão a alcançar pelo Installomator (a aprovada); no Windows, a do
	// instalador (Package).
	Target string
	// Regularize: a atualização troca a instalação de outro tipo (o exe, o bundle) pela do
	// instalador aprovado (Options.Regularize). nil: atualização comum.
	Regularize *Regularization
	// TryOpen: o job tenta instalar com o programa aberto antes de adiar (a regra pede e o
	// agente sabe: Options.CanTryOpen).
	TryOpen bool
}

// Regularization: a instalação que a atualização troca.
type Regularization struct {
	From string            // o tipo instalado: exe, msi ou bundle
	Over bool              // instala por cima (a regra pede regularize: over): nada sai antes
	Old  protocol.Software // a entrada que sai antes da nova (sem Over)
}

// Options: o que o plano pode fazer além da atualização do mesmo tipo.
type Options struct {
	// Regularize: trocar a instalação de outro tipo (ou feita por bundle) pelo instalador
	// aprovado, quando ele é mais novo que a instalada (o parâmetro da regularização).
	Regularize bool
	// CanReplace: o agente da máquina remove a instalação antiga antes de instalar
	// (protocol.CapabilityRegularize). Sem ele, só a regularização por cima.
	CanReplace bool
	// CanTryOpen: o agente da máquina tenta instalar com o programa aberto antes de adiar
	// (protocol.CapabilityTryOpen). Sem ele, o job vai sem try_open (adia, como sempre).
	CanTryOpen bool
}

// TargetVersion: a versão que o job leva.
func (d Decision) TargetVersion() string {
	if d.Package != nil {
		return d.Package.Version
	}
	return d.Target
}

// Plan decide cada programa com "install" encontrado no inventário. arch é a da máquina
// (MachineArch). Programas que a máquina não tem não aparecem.
func Plan(sw []protocol.Software, m thirdparty.Manifest, pkgs []store.ThirdPartyPackage, arch string) []Decision {
	return PlanWith(sw, m, pkgs, arch, Options{})
}

// PlanWith é o Plan com as opções (a regularização).
func PlanWith(sw []protocol.Software, m thirdparty.Manifest, pkgs []store.ThirdPartyPackage, arch string, o Options) []Decision {
	groups := group(sw, m)
	out := make([]Decision, 0, len(groups))
	for _, d := range groups {
		if d.App.PerUser() {
			// Instala no perfil de cada usuário: só a Central, pedido por ele. Nada como SYSTEM.
			d.State = StateUserOnly
			d.Reason = "instala no perfil de cada usuário: só pela Central de software, pedido pelo usuário"
			out = append(out, *d)
			continue
		}
		decide(d, pkgs, arch)
		if o.Regularize && (d.State == StateNoPackage || d.State == StateBundle) {
			regularize(d, pkgs, arch, o)
		}
		if d.State == StateUpdate && d.Regularize == nil && d.App.Install != nil && d.App.Install.UninstallFirst {
			uninstallFirst(d, o)
		}
		if d.State == StateUpdate && o.CanTryOpen && d.Regularize == nil && d.App.Install != nil && d.App.Install.TryOpen {
			d.TryOpen = true
		}
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
	return groupFor("windows", sw, m)
}

// groupFor é o group de um sistema (windows ou macos).
func groupFor(osID string, sw []protocol.Software, m thirdparty.Manifest) []*Decision {
	rules := m.For(osID)
	byApp := map[string]*Decision{}
	var out []*Decision
	for _, s := range sw {
		if s.SystemComponent || s.IsUpdate {
			continue
		}
		i := slices.IndexFunc(rules, func(a thirdparty.App) bool { return a.MatchesSoftware(s) })
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
		if osID == "macos" {
			e.Kind = KindApp // o .app; o Installomator não distingue tipo de instalação
		}
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

// Entries devolve as instalações de um programa num sistema: as de máquina (visíveis) e as de
// usuário. O servidor confere o job thirdparty por elas (Aula 14.4), com o mesmo
// reconhecimento do plano.
func Entries(osID string, sw []protocol.Software, m thirdparty.Manifest, app string) (machine, user []Entry) {
	for _, d := range groupFor(osID, sw, m) {
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
	if d.App.NoVersion() {
		d.State = StateCurrent
		d.Reason = "o programa não informa a versão (se atualiza sozinho): instalado conta como em dia"
		return
	}
	exact := false
	if e.Kind == KindBundle {
		if !slices.Contains(families[KindBundle], d.App.Install.Type) {
			d.State = StateBundle
			d.Reason = "instalado por um bundle (WiX Burn): o MSI direto deixaria a entrada do bundle para trás"
			return
		}
		arch, exact = bundleArch(e.Software.Name, arch)
	}
	if e.Installed == "" {
		d.State = StateBadVersion
		d.Reason = fmt.Sprintf("versão instalada %q não se compara", e.Software.Version)
		return
	}
	best := newest(pkgs, d.App.ID, families[e.Kind], arch, exact)
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
// exact: só a arquitetura arch (o bundle de 32 bits só com o instalador de 32).
func newest(pkgs []store.ThirdPartyPackage, app string, types []string, arch string, exact bool) *store.ThirdPartyPackage {
	var best *store.ThirdPartyPackage
	for i := range pkgs {
		p := &pkgs[i]
		if p.AppID != app || !slices.Contains(types, p.Type) || winget.ArchRank(arch, p.Architecture) < 0 || exact && p.Architecture != arch {
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

// regularize troca a decisão "sem pacote do tipo" ou "bundle" pela atualização com o
// instalador aprovado de outro tipo, quando ele é mais novo que a versão instalada (a
// regularização nunca rebaixa) e a instalação antiga sai em silêncio (ou a regra instala por
// cima). Sem como, a decisão fica e o motivo diz o que falta.
func regularize(d *Decision, pkgs []store.ThirdPartyPackage, arch string, o Options) {
	if len(d.Entries) != 1 || d.App.Install == nil || d.App.OS != "windows" {
		return
	}
	e := d.Entries[0]
	pk := ForNewInstall(d.App, pkgs, arch)
	if pk == nil {
		return
	}
	over := d.App.Install.Regularize == thirdparty.RegularizeOver
	switch {
	case !over && e.Software.Uninstall == "":
		d.Reason += "; para regularizar, a instalação de agora precisa de desinstalação silenciosa registrada (não tem, ou o agente ainda não informa: atualize o agente)"
		return
	case !over && !o.CanReplace:
		d.Reason += "; para regularizar, atualize o agente desta máquina"
		return
	}
	pv, ok := thirdparty.Normalize(pk.Version)
	if !ok || e.Installed == "" {
		return
	}
	if c, err := version.CompareDotted(e.Installed, pv); err != nil || c >= 0 {
		// A instalada é da versão aprovada ou mais nova: a regularização não rebaixa.
		d.State, d.Package = StateCurrent, pk
		d.Reason = fmt.Sprintf("%s instalado (%s); a regularização para %s vem com a próxima versão aprovada mais nova que esta",
			e.Installed, KindLabel(e.Kind), pk.Type)
		return
	}
	d.State, d.Package, d.Installed = StateUpdate, pk, e.Installed
	d.Regularize = &Regularization{From: e.Kind, Over: over, Old: e.Software}
	how := "remove a instalação " + KindLabel(e.Kind) + " e instala"
	if over {
		how = "instala por cima da " + KindLabel(e.Kind)
	}
	d.Reason = fmt.Sprintf("%s → %s · %s · %s · regularizar: %s", e.Installed, pk.Version, pk.Type, pk.Architecture, how)
}

// uninstallFirst: a atualização do mesmo tipo que a regra manda fazer removendo antes a
// instalação de agora (Install.UninstallFirst), com a mesma remoção silenciosa da
// regularização. Sem como remover, a atualização fica comum e o motivo diz o que falta.
func uninstallFirst(d *Decision, o Options) {
	if len(d.Entries) != 1 {
		return
	}
	e := d.Entries[0]
	switch {
	case e.Software.Uninstall == "":
		d.Reason += "; a regra pede remover antes, mas a instalação de agora não tem desinstalação silenciosa registrada (ou o agente ainda não informa: atualize o agente): vai por cima"
		return
	case !o.CanReplace:
		d.Reason += "; a regra pede remover antes: atualize o agente desta máquina (até lá, vai por cima)"
		return
	}
	d.Regularize = &Regularization{From: e.Kind, Old: e.Software}
	d.Reason += " · remove a instalação de agora e instala"
}

// NeedsReplace: o plano precisa saber se o agente remove a instalação antiga (Options.CanReplace)
// mesmo com a regularização desligada, porque alguma regra pede remover antes.
func NeedsReplace(m thirdparty.Manifest) bool {
	for _, a := range m.For("windows") {
		if a.Install != nil && a.Install.UninstallFirst {
			return true
		}
	}
	return false
}

// NeedsCaps: o plano precisa das capacidades do agente (Options.CanReplace, CanTryOpen) mesmo
// com a regularização desligada: alguma regra pede remover antes ou tentar com o programa aberto.
func NeedsCaps(m thirdparty.Manifest) bool {
	if NeedsReplace(m) {
		return true
	}
	for _, a := range m.For("windows") {
		if a.Install != nil && a.Install.TryOpen {
			return true
		}
	}
	return false
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
	if app.PerUser() {
		return nil // o instalador por usuário não vai como SYSTEM (ForUserInstall, pela Central)
	}
	return forInstall(app, pkgs, arch)
}

// ForUserInstall: o instalador do programa por usuário (PerUser), que a Central roda como o
// usuário: para instalar e para atualizar (o instalador novo por cima da instalação dele).
func ForUserInstall(app thirdparty.App, pkgs []store.ThirdPartyPackage, arch string) *store.ThirdPartyPackage {
	if !app.PerUser() {
		return nil
	}
	return forInstall(app, pkgs, arch)
}

func forInstall(app thirdparty.App, pkgs []store.ThirdPartyPackage, arch string) *store.ThirdPartyPackage {
	types := append(slices.Clone(families[KindExe]), families[KindMSI]...)
	if app.Install != nil && app.Install.Type != "" {
		// A família do tipo da regra: o que importa é MSI ou exe (o padrão da frota), não o
		// gerador do exe (o VLC da regra nullsoft com o exe do site do fabricante).
		types = FamilyOf(app.Install.Type)
	}
	return newest(pkgs, app.ID, types, arch, false)
}
