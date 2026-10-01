package protocol

import "strings"

// appendErrors acrescenta cada linha de err como uma entrada própria, com o prefixo da
// seção. errors.Join junta mensagens com quebra de linha; aqui elas se separam.
func appendErrors(dst []string, section string, err error) []string {
	if err == nil {
		return dst
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dst = append(dst, section+": "+line)
		}
	}
	return dst
}

// AddErrors registra as falhas de uma seção do inventário, uma por entrada.
func (r *InventoryReport) AddErrors(section string, err error) {
	r.Errors = appendErrors(r.Errors, section, err)
}

// AddErrors registra as falhas de uma seção da busca de atualizações, uma por entrada.
func (r *PatchScanReport) AddErrors(section string, err error) {
	r.Errors = appendErrors(r.Errors, section, err)
}
