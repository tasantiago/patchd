package inventory

import (
	"context"
	"errors"
)

// Software junta os aplicativos (system_profiler) e os recibos de instalação (pkgutil).
// Nenhuma das duas fontes basta sozinha: apps arrastados não deixam recibo, e muitos
// pacotes instalam ferramentas que não são aplicativos. Uma fonte pode falhar sem a outra.
func (c *darwinCollector) Software(ctx context.Context) ([]Software, error) {
	apps, errApps := collectDarwinApps(ctx, c.run)
	receipts, errReceipts := collectDarwinReceipts(ctx, c.run)

	list := append(apps, receipts...)
	sortSoftware(list)
	return list, errors.Join(errApps, errReceipts)
}
