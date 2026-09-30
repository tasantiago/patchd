package inventory

import "context"

// Hardware lê o system_profiler (modelo, serial, UUID, firmware) e o sysctl (números).
func (c *darwinCollector) Hardware(ctx context.Context) (Hardware, error) {
	return collectDarwinHardware(ctx, c.run)
}
