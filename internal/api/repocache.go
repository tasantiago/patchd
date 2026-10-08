package api

import (
	"slices"

	"github.com/tasantiago/patchd/internal/protocol"
)

// WithRepoCache faz o check-in anunciar o cache de repositórios (Aula 6.6): os agentes Linux
// passam a configurar o apt (proxy só para aptHosts) e o dnf (baseurl) para usá-lo. url é
// como as máquinas alcançam o servidor, que nem sempre é o endereço em que ele escuta.
func WithRepoCache(url string, aptHosts []string) Option {
	return func(a *api) {
		hs := slices.Clone(aptHosts)
		slices.Sort(hs)
		a.repoCache = &protocol.RepoCache{URL: url, AptHosts: hs}
	}
}
