package inventory

import "context"

// Hardware: provisório; a leitura via system_profiler SPHardwareDataType vem na Aula 2.4.
func (c *darwinCollector) Hardware(ctx context.Context) (Hardware, error) {
	return Hardware{}, ErrNotImplemented
}
