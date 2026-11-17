package api

import (
	"strings"
	"sync"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Os atalhos na área de trabalho de todos os usuários (protocol.DesktopShortcut): os das
// regras de terceiros do Windows que pedem um. A lista vai em cada check-in; é a mesma para
// todas as máquinas, e o agente só cria o atalho onde o executável existe. Ela é montada uma
// vez por versão do manifesto vivo.

type desktopCache struct {
	mu   sync.Mutex
	gen  uint64
	ok   bool
	list *protocol.DesktopShortcuts
}

// desktopShortcuts: a lista do manifesto de agora; nil sem o manifesto (o agente mantém o que tem).
func (a *api) desktopShortcuts() *protocol.DesktopShortcuts {
	if a.tpManifest == nil {
		return nil
	}
	c := &a.desktop
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen := a.tpManifest.Gen(); !c.ok || c.gen != gen {
		c.list, c.gen, c.ok = a.buildDesktopShortcuts(), gen, true
	}
	return c.list
}

func (a *api) buildDesktopShortcuts() *protocol.DesktopShortcuts {
	list := &protocol.DesktopShortcuts{Items: []protocol.DesktopShortcut{}}
	seen := map[string]string{}
	for _, r := range a.tpManifest.For("windows") {
		sc := r.DesktopShortcut()
		if sc == nil {
			continue
		}
		key := strings.ToLower(sc.Name)
		switch {
		case seen[key] != "":
			a.logger.Warn("atalho na área de trabalho com o nome de outro programa: fica o primeiro", "app", r.ID, "outro", seen[key], "nome", sc.Name)
			continue
		case len(list.Items) >= protocol.MaxDesktopShortcuts:
			a.logger.Warn("atalhos na área de trabalho: lista cheia", "max", protocol.MaxDesktopShortcuts, "app", r.ID)
			continue
		case sc.Validate() != nil: // a regra já foi conferida; por garantia
			continue
		}
		seen[key] = r.ID
		list.Items = append(list.Items, *sc)
	}
	return list
}
