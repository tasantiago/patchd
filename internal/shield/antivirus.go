package shield

import (
	"fmt"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// AntivirusServices is vendor-neutral. All listed services must run.
type AntivirusServices struct {
	Windows []string `json:"windows,omitempty"`
	Linux   []string `json:"linux,omitempty"`
	MacOS   []string `json:"macos,omitempty"`
}

func (a AntivirusServices) Validate() error {
	for _, list := range [][]string{a.Windows, a.Linux, a.MacOS} {
		if len(list) > 32 {
			return fmt.Errorf("antivírus: máximo de 32 serviços por sistema")
		}
		for _, name := range list {
			if !protocol.ValidServiceName(name) {
				return fmt.Errorf("antivírus: nome de serviço inválido")
			}
		}
	}
	return nil
}

func (a AntivirusServices) Evaluate(s *protocol.RunningServices, at *time.Time, now time.Time) (*bool, string) {
	if s == nil || at == nil || at.IsZero() || now.Sub(*at) > 48*time.Hour || at.After(now.Add(time.Minute)) || s.Check() != nil {
		return nil, "sem coleta recente de serviços"
	}
	var required []string
	switch s.Family {
	case "windows":
		required = a.Windows
	case "linux":
		required = a.Linux
	case "darwin":
		required = a.MacOS
	}
	if len(required) == 0 {
		return nil, "serviços do antivírus não configurados para este sistema"
	}
	var missing []string
	for _, name := range required {
		found := false
		for _, running := range s.Names {
			if running == name || (s.Family == "windows" && strings.EqualFold(running, name)) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	ok := len(missing) == 0
	if !ok {
		return &ok, "serviço(s) obrigatório(s) não encontrado(s) em execução: " + strings.Join(missing, ", ")
	}
	return &ok, fmt.Sprintf("%d serviço(s) obrigatório(s) em execução", len(required))
}
