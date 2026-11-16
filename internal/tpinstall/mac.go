package tpinstall

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

// Programas de terceiros no macOS, pelo Installomator: a regra tem install.installomator (o
// rótulo), o servidor guarda o script (internal/installomator) e o job thirdparty leva o
// script e o rótulo. O Installomator instala a versão mais nova do fabricante; o job leva a
// versão aprovada como a mínima que o servidor confere no inventário de depois.

// MacScript: o Installomator do cache do servidor, já conferido.
type MacScript struct {
	SHA256 string
	Size   int64
}

// PlanMac decide os programas com install.installomator encontrados no inventário de um Mac.
// target devolve a versão a alcançar de cada programa (a aprovada); vazia: nenhuma.
func PlanMac(sw []protocol.Software, m thirdparty.Manifest, target func(app string) string) []Decision {
	groups := groupFor("macos", sw, m)
	out := make([]Decision, 0, len(groups))
	for _, d := range groups {
		decideMac(d, target(d.App.ID))
		out = append(out, *d)
	}
	slices.SortStableFunc(out, func(a, b Decision) int {
		return cmp.Or(cmp.Compare(slices.Index(States, a.State), slices.Index(States, b.State)), strings.Compare(a.App.Name, b.App.Name))
	})
	return out
}

func decideMac(d *Decision, target string) {
	label := d.App.Install.Installomator
	switch {
	case len(d.Entries) == 0:
		d.State = StateUserOnly
		d.Reason = fmt.Sprintf("%d instalação(ões) na pasta do usuário: o Installomator atualiza a de /Applications", len(d.User))
		return
	case len(d.Entries) > 1:
		var parts []string
		for _, e := range d.Entries {
			parts = append(parts, cmp.Or(e.Software.Version, "?"))
		}
		d.State = StateDuplicate
		d.Reason = fmt.Sprintf("%d cópias do programa (%s): deixe uma", len(d.Entries), strings.Join(parts, ", "))
		return
	}
	e := d.Entries[0]
	d.Installed = e.Installed
	if e.Installed == "" {
		d.State = StateBadVersion
		d.Reason = fmt.Sprintf("versão instalada %q não se compara", e.Software.Version)
		return
	}
	t, ok := thirdparty.Normalize(target)
	if !ok {
		d.State = StateNoPackage
		d.Reason = "nenhuma versão aprovada: aprove na página do programa (o Installomator instala a mais nova do fabricante)"
		return
	}
	d.Target = target
	if c, err := version.CompareDotted(e.Installed, t); err == nil && c >= 0 {
		d.State = StateCurrent
		d.Reason = fmt.Sprintf("%s instalado; a aprovada é %s", e.Installed, target)
		return
	}
	d.State = StateUpdate
	d.Reason = fmt.Sprintf("%s → %s ou mais nova · Installomator (%s)", e.Installed, target, label)
}

// MacJobParams monta o job thirdparty do Mac a partir de uma decisão ATUALIZAR.
func MacJobParams(d Decision, s MacScript) (jobs.ThirdPartyParams, error) {
	if d.State != StateUpdate || d.Target == "" || d.App.Install == nil || d.App.Install.Installomator == "" {
		return jobs.ThirdPartyParams{}, fmt.Errorf("a decisão não é ATUALIZAR pelo Installomator")
	}
	p := jobs.ThirdPartyParams{App: d.App.ID, Version: d.Target, SHA256: s.SHA256, Size: s.Size,
		Installer: jobs.InstallerInstallomator, Args: []string{d.App.Install.Installomator}}
	return p, p.Validate()
}
