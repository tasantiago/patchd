// Package config lê a configuração do patchd a partir de variáveis de ambiente.
// Precedência adotada nos binários: flag > variável de ambiente > padrão.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Lookup busca uma variável de ambiente. Em produção é os.LookupEnv; nos testes, um mapa.
type Lookup func(key string) (string, bool)

// String devolve o valor da variável key, ou def se ela não existir ou estiver vazia.
func String(look Lookup, key, def string) string {
	if v, ok := look(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Duration lê uma duração no formato do Go (por exemplo 90s, 15m ou 1h30m).
// Se a variável não existir, devolve def sem erro.
func Duration(look Lookup, key string, def time.Duration) (time.Duration, error) {
	v, ok := look(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return def, fmt.Errorf("%s: duração inválida %q (use por exemplo 90s, 15m ou 1h)", key, v)
	}
	return d, nil
}

// Secret lê um segredo da variável key ou do arquivo indicado em key_FILE
// (padrão dos secrets do Docker). Definir os dois ao mesmo tempo é erro,
// para não haver dúvida sobre qual valor vale. Não existe versão por flag:
// a linha de comando de um processo é visível para outros usuários.
func Secret(look Lookup, key string) (string, error) {
	value, hasValue := look(key)
	file, hasFile := look(key + "_FILE")
	hasValue = hasValue && value != ""
	file = strings.TrimSpace(file)
	hasFile = hasFile && file != ""

	switch {
	case hasValue && hasFile:
		return "", fmt.Errorf("defina apenas um entre %s e %s_FILE", key, key)
	case hasFile:
		content, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: não foi possível ler o arquivo: %w", key, err)
		}
		secret := strings.TrimSpace(string(content))
		if secret == "" {
			return "", fmt.Errorf("%s_FILE: o arquivo está vazio", key)
		}
		return secret, nil
	case hasValue:
		return value, nil
	default:
		return "", nil
	}
}
