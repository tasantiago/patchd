package tpinstall

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/installomator"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

// CampaignStore é o que a campanha de terceiros lê do banco.
type CampaignStore interface {
	LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	ThirdPartyApprovals(ctx context.Context) ([]store.ThirdPartyApproval, error)
	MachineCapabilities(ctx context.Context, id string) ([]string, error)
}

// Campaigner decide a campanha de terceiros (Aula 14.7): uma versão aprovada de um
// programa, levada pelos grupos de implantação. Cada máquina passa pelo mesmo plano do painel e do
// package plan, só que com os instaladores guardados daquela versão: quem já está nela
// (ou acima) fica "em dia"; duplicado, bundle e os outros casos ficam fora, com o motivo.
type Campaigner struct {
	Store    CampaignStore
	Manifest thirdparty.Manifest
	// Rules: as regras que valem agora (o programa incluído pelo painel entra na hora); nil:
	// vale Manifest.
	Rules *thirdparty.Live
	Dir   string // PATCHD_CONTENT_DIR/pacotes; vazio: o servidor não guarda instaladores
	// Installomator: a versão do script que vai aos Macs (internal/installomator); nil: os
	// Macs ficam fora das campanhas de terceiros.
	Installomator *installomator.Script
}

// rules: o manifesto de agora.
func (c Campaigner) rules() thirdparty.Manifest {
	if c.Rules != nil {
		return c.Rules.Get()
	}
	return c.Manifest
}

// macRule: a regra do macOS do programa, com o rótulo do Installomator.
func (c Campaigner) macRule(app string) *thirdparty.App {
	for _, r := range c.rules().For("macos") {
		if r.ID == app && r.Install != nil && r.Install.Installomator != "" {
			return &r
		}
	}
	return nil
}

// MacScript confere o Installomator no cache: o script e o motivo de não haver.
func (c Campaigner) MacScript() (*MacScript, string) {
	switch {
	case c.Installomator == nil:
		return nil, "o servidor está sem o Installomator configurado"
	case c.Dir == "":
		return nil, "o servidor não guarda instaladores (PATCHD_CONTENT_DIR)"
	}
	n, err := installomator.Check(c.Dir, *c.Installomator)
	if err != nil {
		return nil, fmt.Sprintf("o Installomator %s não está no cache ou não confere (%v): catalog sync -source terceiros", c.Installomator.Version, err)
	}
	return &MacScript{SHA256: c.Installomator.SHA256, Size: n}, ""
}

func (c Campaigner) rule(app string) *thirdparty.App {
	for _, r := range c.rules().For("windows") {
		if r.ID == app && r.Install != nil {
			return &r
		}
	}
	return nil
}

// versionPackages: os instaladores guardados de uma versão do programa (um por tipo e
// arquitetura).
func versionPackages(all []store.ThirdPartyPackage, app, version string) []store.ThirdPartyPackage {
	var out []store.ThirdPartyPackage
	for _, p := range all {
		if p.AppID == app && p.Version == version {
			out = append(out, p)
		}
	}
	return out
}

// Ready confere a versão antes de cada grupo de implantação: regra com install, aprovação em vigor, ao
// menos um instalador guardado, cada um conferindo pelo SHA-256 no cache e cabendo num job.
// reason vazio: pronta. err é falha de leitura (o laço tenta de novo no minuto seguinte).
func (c Campaigner) Ready(ctx context.Context, app, version string) (string, error) {
	rule := c.rule(app)
	mac := c.macRule(app)
	if rule == nil && mac == nil {
		return fmt.Sprintf("o programa %q não tem \"install\" nas regras", app), nil
	}
	approvals, err := c.Store.ThirdPartyApprovals(ctx)
	if err != nil {
		return "", err
	}
	approved := false
	for _, a := range approvals {
		approved = approved || a.AppID == app && a.Version == version
	}
	if !approved {
		return fmt.Sprintf("%s %s não está aprovado (a aprovação pode ter sido revogada)", app, version), nil
	}
	if rule == nil { // só Mac: vale o Installomator no cache
		_, why := c.MacScript()
		return why, nil
	}
	all, err := c.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return "", err
	}
	pkgs := versionPackages(all, app, version)
	if len(pkgs) == 0 && mac != nil {
		// Sem instalador do Windows guardado, a campanha anda pelos Macs (os do Windows ficam
		// "sem pacote"), se o Installomator estiver no cache.
		_, why := c.MacScript()
		return why, nil
	}
	switch {
	case len(pkgs) == 0:
		return fmt.Sprintf("nenhum instalador de %s %s guardado (package fetch)", app, version), nil
	case c.Dir == "":
		return "o servidor não guarda instaladores (PATCHD_CONTENT_DIR)", nil
	}
	for i := range pkgs {
		pk := &pkgs[i]
		if _, err := pkgcache.Check(c.Dir, pk.SHA256, pk.MSI()); err != nil {
			return fmt.Sprintf("o instalador %s/%s de %s %s não confere no cache (%v): package fetch -app %s", pk.Type, pk.Architecture, app, version, err, app), nil
		}
		if _, err := JobParams(Decision{App: *rule, State: StateUpdate, Package: pk}); err != nil {
			return fmt.Sprintf("o instalador %s/%s de %s %s não cabe num job: %v", pk.Type, pk.Architecture, app, version, err), nil
		}
	}
	return "", nil
}

// Job decide a máquina: os parâmetros do job, ou por que ela fica fora.
func (c Campaigner) Job(ctx context.Context, machineID, app, version string) (*jobs.ThirdPartyParams, string, error) {
	inv, found, err := c.Store.LatestInventory(ctx, machineID)
	switch {
	case err != nil:
		return nil, "", err
	case found && inv.OS.ID == "macos":
		return c.macJob(ctx, machineID, inv, app, version)
	case !found || inv.OS.ID != "windows":
		return nil, "sem inventário do Windows ou do macOS", nil
	}
	arch, ok := MachineArch(inv.OS.Arch)
	if !ok {
		return nil, "arquitetura desconhecida", nil
	}
	all, err := c.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, d := range Plan(inv.Software, c.rules(), versionPackages(all, app, version), arch) {
		if d.App.ID != app {
			continue
		}
		if d.State != StateUpdate {
			return nil, strings.ToLower(Labels[d.State]), nil
		}
		p, err := JobParams(d)
		if err != nil {
			return nil, "instalador fora do job", nil
		}
		return &p, "", nil
	}
	return nil, "sem o programa", nil
}

// macJob decide um Mac: o job do Installomator, ou por que ele fica fora.
func (c Campaigner) macJob(ctx context.Context, machineID string, inv protocol.InventoryReport, app, version string) (*jobs.ThirdPartyParams, string, error) {
	if c.macRule(app) == nil {
		return nil, "sem regra do Installomator para o macOS", nil
	}
	caps, err := c.Store.MachineCapabilities(ctx, machineID)
	if err != nil {
		return nil, "", err
	}
	if !slices.Contains(caps, protocol.CapabilityInstallomator) {
		return nil, "agente do Mac sem o Installomator (atualize o agente)", nil
	}
	script, why := c.MacScript()
	if script == nil {
		return nil, why, nil
	}
	for _, d := range PlanMac(inv.Software, c.rules(), func(string) string { return version }) {
		if d.App.ID != app {
			continue
		}
		if d.State != StateUpdate {
			return nil, strings.ToLower(Labels[d.State]), nil
		}
		p, err := MacJobParams(d, *script)
		if err != nil {
			return nil, "job do Installomator inválido", nil
		}
		return &p, "", nil
	}
	return nil, "sem o programa", nil
}

// Approved lista, para a campanha de todos os programas aprovados, a versão aprovada mais nova
// de cada programa que tem "install" nas regras (implementa campaign.ThirdPartyCatalog). Se a
// versão está pronta, quem decide é o Ready, a cada grupo de implantação.
func (c Campaigner) Approved(ctx context.Context) ([]campaign.AppVersion, error) {
	approvals, err := c.Store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, err
	}
	best := map[string]string{}
	for _, a := range approvals {
		if c.rule(a.AppID) == nil && c.macRule(a.AppID) == nil {
			continue
		}
		cur, ok := best[a.AppID]
		if !ok {
			best[a.AppID] = a.Version
			continue
		}
		if n, err := version.CompareDotted(a.Version, cur); (err == nil && n > 0) || (err != nil && a.Version > cur) {
			best[a.AppID] = a.Version
		}
	}
	out := make([]campaign.AppVersion, 0, len(best))
	for _, app := range slices.Sorted(maps.Keys(best)) {
		out = append(out, campaign.AppVersion{App: app, Version: best[app]})
	}
	return out, nil
}
