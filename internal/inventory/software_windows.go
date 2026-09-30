package inventory

import "context"

// Software: provisório; a leitura das chaves Uninstall vem na segunda etapa da Aula 2.2.
func (c *windowsCollector) Software(ctx context.Context) ([]Software, error) {
	return nil, ErrNotImplemented
}
