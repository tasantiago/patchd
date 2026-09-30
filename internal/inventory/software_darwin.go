package inventory

import "context"

// Software lista os aplicativos pelo system_profiler. Os recibos do pkgutil
// entram na segunda etapa da Aula 2.4.
func (c *darwinCollector) Software(ctx context.Context) ([]Software, error) {
	list, err := collectDarwinApps(ctx, c.run)
	sortSoftware(list)
	return list, err
}
