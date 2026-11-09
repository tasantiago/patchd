package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// TypeCleanup (limpeza de temporários): o agente apaga, de uma lista fechada de itens que ele
// mesmo conhece, só arquivos antigos (mais de CleanupMinAge) e só de pastas de temporário e de
// cache. O job leva os itens, nunca caminhos: ninguém aponta a limpeza para outra pasta.
const TypeCleanup = "limpeza"

// Itens da limpeza.
const (
	CleanupSystemTemp  = "temp_sistema"       // Windows: %SystemRoot%\Temp; Linux: /tmp e /var/tmp (systemd-tmpfiles)
	CleanupUserTemp    = "temp_usuarios"      // Windows: o Temp de cada perfil (AppData\Local\Temp)
	CleanupPatchd      = "patchd"             // instaladores e pastas temporárias antigas dos jobs do agente
	CleanupUpdateCache = "cache_atualizacoes" // Windows: SoftwareDistribution\Download; Linux: o cache do apt/dnf
	CleanupComponents  = "componentes"        // Windows: DISM /StartComponentCleanup; Linux: o journal acima de 200 MB
)

// CleanupItems: todos, na ordem da tela.
var CleanupItems = []string{CleanupSystemTemp, CleanupUserTemp, CleanupPatchd, CleanupUpdateCache, CleanupComponents}

// CleanupDefault: os marcados por padrão (os rápidos e sem efeito colateral).
var CleanupDefault = []string{CleanupSystemTemp, CleanupUserTemp, CleanupPatchd}

// CleanupLabel: o item para pessoas.
func CleanupLabel(item string) string {
	switch item {
	case CleanupSystemTemp:
		return "temporários do sistema"
	case CleanupUserTemp:
		return "temporários dos usuários"
	case CleanupPatchd:
		return "restos do patchd"
	case CleanupUpdateCache:
		return "cache de download de atualizações"
	case CleanupComponents:
		return "limpeza de componentes (DISM)"
	}
	return item
}

// CleanupParams: os itens a limpar.
type CleanupParams struct {
	Items []string `json:"items"`
}

// ParseCleanupParams lê e confere.
func ParseCleanupParams(raw json.RawMessage) (CleanupParams, error) {
	var p CleanupParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros da limpeza ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate: de 1 a todos os itens conhecidos, sem repetir.
func (p CleanupParams) Validate() error {
	if len(p.Items) == 0 || len(p.Items) > len(CleanupItems) {
		return fmt.Errorf("a limpeza leva de 1 a %d itens", len(CleanupItems))
	}
	for i, it := range p.Items {
		if !slices.Contains(CleanupItems, it) {
			return fmt.Errorf("item de limpeza desconhecido %q", it)
		}
		if slices.Contains(p.Items[:i], it) {
			return fmt.Errorf("item de limpeza repetido %q", it)
		}
	}
	return nil
}
