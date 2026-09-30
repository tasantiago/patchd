package inventory

import "context"

// Software: provisório; a leitura via system_profiler e pkgutil vem na Aula 2.4.
func (c *darwinCollector) Software(ctx context.Context) ([]Software, error) {
	return nil, ErrNotImplemented
}
