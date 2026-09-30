package inventory

import "context"

// Software: provisório; a leitura via dpkg-query e rpm vem na Aula 2.3.
func (c *linuxCollector) Software(ctx context.Context) ([]Software, error) {
	return nil, ErrNotImplemented
}
