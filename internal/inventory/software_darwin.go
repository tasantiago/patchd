package inventory

import (
	"context"
	"errors"
)

// Software junta três fontes: os aplicativos do system_profiler, os aplicativos do
// cryptex (Safari, que o system_profiler não lista) e os recibos do pkgutil.
// Nenhuma basta sozinha, e cada uma pode falhar sem derrubar as outras.
func (c *darwinCollector) Software(ctx context.Context) ([]Software, error) {
	apps, errApps := collectDarwinApps(ctx, c.run)
	cryptex, errCryptex := collectCryptexApps(ctx, c.run, listDirNames)
	receipts, errReceipts := collectDarwinReceipts(ctx, c.run)

	// Mesmo caminho não entra duas vezes, caso o system_profiler passe a listar o cryptex.
	seen := make(map[string]bool, len(apps))
	for _, a := range apps {
		seen[a.Source] = true
	}
	list := apps
	for _, a := range cryptex {
		if !seen[a.Source] {
			list = append(list, a)
		}
	}
	list = append(list, receipts...)

	sortSoftware(list)
	return list, errors.Join(errApps, errCryptex, errReceipts)
}
