package inventory

import "context"

// Hardware: provisório; serial e UUID dependem do WMI, que exige COM (Aula 3.1).
func (c *windowsCollector) Hardware(ctx context.Context) (Hardware, error) {
	return Hardware{}, ErrNotImplemented
}
