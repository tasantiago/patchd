package inventory

import (
	"context"
	"fmt"
)

// Software consulta o banco de pacotes da distribuição (dpkg ou rpm), escolhido pelo os-release.
// Em falha parcial (linhas ilegíveis, pacotes quebrados), devolve a lista junto com o erro.
func (c *linuxCollector) Software(ctx context.Context) ([]Software, error) {
	fields, _, err := c.readOSRelease()
	if err != nil {
		return nil, err
	}

	var list []Software
	switch packageManagerFor(fields["ID"], fields["ID_LIKE"]) {
	case "dpkg":
		// Sem shell: o formato chega literalmente ao dpkg-query, sem expansão de ${...}.
		out, err := c.run.Run(ctx, dpkgQueryPath, "-W", "--showformat="+dpkgFormat)
		if err != nil {
			return nil, err
		}
		list, err = parseDpkgQuery(out)
		sortSoftware(list)
		return list, err
	case "rpm":
		out, err := c.run.Run(ctx, rpmPath, "-qa", "--queryformat", rpmFormat)
		if err != nil {
			return nil, err
		}
		list, err = parseRPMQuery(out)
		sortSoftware(list)
		return list, err
	default:
		return nil, fmt.Errorf("distribuição %q sem inventário de pacotes: %w", fields["ID"], ErrNotImplemented)
	}
}
