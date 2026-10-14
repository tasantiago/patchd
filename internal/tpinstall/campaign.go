package tpinstall

import (
	"context"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// CampaignStore é o que a campanha de terceiros lê do banco.
type CampaignStore interface {
	LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	ThirdPartyApprovals(ctx context.Context) ([]store.ThirdPartyApproval, error)
}

// Campaigner decide a campanha de terceiros (Aula 14.7): uma versão aprovada de um
// programa, levada pelos anéis. Cada máquina passa pelo mesmo plano do painel e do
// package plan, só que com os instaladores guardados daquela versão: quem já está nela
// (ou acima) fica "em dia"; duplicado, bundle e os outros casos ficam fora, com o motivo.
type Campaigner struct {
	Store    CampaignStore
	Manifest thirdparty.Manifest
	Dir      string // PATCHD_CONTENT_DIR/pacotes; vazio: o servidor não guarda instaladores
}

func (c Campaigner) rule(app string) *thirdparty.App {
	for _, r := range c.Manifest.For("windows") {
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

// Ready confere a versão antes de cada anel: regra com install, aprovação em vigor, ao
// menos um instalador guardado, cada um conferindo pelo SHA-256 no cache e cabendo num job.
// reason vazio: pronta. err é falha de leitura (o laço tenta de novo no minuto seguinte).
func (c Campaigner) Ready(ctx context.Context, app, version string) (string, error) {
	rule := c.rule(app)
	if rule == nil {
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
	all, err := c.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return "", err
	}
	pkgs := versionPackages(all, app, version)
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
	case !found || inv.OS.ID != "windows":
		return nil, "sem inventário do Windows", nil
	}
	arch, ok := MachineArch(inv.OS.Arch)
	if !ok {
		return nil, "arquitetura desconhecida", nil
	}
	all, err := c.Store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, d := range Plan(inv.Software, c.Manifest, versionPackages(all, app, version), arch) {
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
