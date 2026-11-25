package protocol

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxRunningServices = 2048
const MaxServiceName = 256

// RunningServices is a successful snapshot of running service names.
// A nil snapshot means collection failed or is unsupported.
type RunningServices struct {
	Family string   `json:"family"`
	Names  []string `json:"names"`
}

func ValidServiceName(s string) bool {
	return s != "" && len(s) <= MaxServiceName && utf8.ValidString(s) &&
		strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (s RunningServices) Check() error {
	if s.Family != "windows" && s.Family != "linux" && s.Family != "darwin" {
		return fmt.Errorf("família de serviços inválida")
	}
	if s.Names == nil || len(s.Names) > MaxRunningServices {
		return fmt.Errorf("lista de serviços ausente ou grande demais")
	}
	for _, n := range s.Names {
		if !ValidServiceName(n) {
			return fmt.Errorf("nome de serviço inválido")
		}
	}
	return nil
}
