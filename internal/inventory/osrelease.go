package inventory

import "strings"

// parseOSRelease interpreta o formato do os-release(5): linhas CHAVE=valor,
// comentários com #, valores opcionalmente entre aspas duplas (com escapes \" \\ \$ \`)
// ou simples. Linhas fora do formato são ignoradas.
func parseOSRelease(data []byte) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			switch {
			case first == '"' && last == '"':
				value = unescapeDoubleQuoted(value[1 : len(value)-1])
			case first == '\'' && last == '\'':
				value = value[1 : len(value)-1]
			}
		}
		fields[key] = value
	}
	return fields
}

// unescapeDoubleQuoted remove a barra invertida antes de " \ $ e `, como no shell.
func unescapeDoubleQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
