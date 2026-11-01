package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	CapabilityStandardization   = "standardization-v1"
	StandardizationPath         = "/api/v1/agent/standardization"
	StandardizationReportPath   = "/api/v1/agent/standardization/report"
	MaxStandardizationShortcuts = 24
	MaxStandardizationApps      = 64
)

type StandardShortcut struct {
	OS     string `json:"os,omitempty"` // vazio: todos os sistemas compatíveis com o destino
	Name   string `json:"name"`
	Kind   string `json:"kind"` // site, aplicativo ou pasta
	Target string `json:"target"`
	Icon   string `json:"icon,omitempty"` // padrão, site, aplicativo, pasta ou documento
}

var shortcutDrive = regexp.MustCompile(`^[A-Za-z]:\\`)
var shortcutEnv = regexp.MustCompile(`(?i)^%(ProgramFiles|ProgramFiles\(x86\)|LocalAppData|UserProfile|SystemRoot)%\\`)
var shortcutReserved = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\.|$)`)

func (s StandardShortcut) Validate() error {
	if s.OS != "" && s.OS != "windows" && s.OS != "linux" && s.OS != "darwin" {
		return errors.New("sistema do atalho inválido")
	}
	if strings.TrimSpace(s.Name) != s.Name || s.Name == "" || len([]rune(s.Name)) > 64 || strings.ContainsAny(s.Name, `<>:"/\|?*`) || strings.HasSuffix(s.Name, ".") || shortcutReserved.MatchString(s.Name) || invalidStandardText(s.Name) {
		return errors.New("nome do atalho inválido: use até 64 caracteres válidos para um arquivo do Windows")
	}
	if s.Target == "" || len(s.Target) > 1024 || invalidStandardText(s.Target) {
		return errors.New("destino do atalho inválido")
	}
	switch s.Icon {
	case "", "site", "aplicativo", "pasta", "documento":
	default:
		return errors.New("ícone fora da lista de padrões")
	}
	switch s.Kind {
	case "site":
		u, err := url.Parse(s.Target)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return errors.New("site exige URL http ou https, sem credencial embutida")
		}
	case "aplicativo", "pasta":
		if strings.HasPrefix(s.Target, "/") || strings.HasPrefix(s.Target, "~/") {
			if s.OS == "windows" || strings.ContainsAny(s.Target, `"`+"`$\\") || strings.Contains(s.Target, "/../") || strings.HasSuffix(s.Target, "/..") {
				return errors.New("caminho Unix inválido; use caminho absoluto ou ~/ sem expansão de comandos")
			}
			return nil
		}
		if s.OS == "linux" || s.OS == "darwin" {
			return errors.New("use um caminho absoluto Unix ou ~/ para Linux e macOS")
		}
		if strings.ContainsAny(s.Target, `<>"|?*`) || strings.HasPrefix(s.Target, `\\?\`) || strings.HasPrefix(s.Target, `\\.\`) {
			return errors.New("caminho do atalho inválido")
		}
		path := s.Target
		switch {
		case shortcutDrive.MatchString(path):
			path = path[2:]
		case shortcutEnv.MatchString(path):
		case strings.HasPrefix(path, `\\`):
			parts := strings.Split(strings.TrimPrefix(path, `\\`), `\`)
			if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
				return errors.New("pasta de rede exige servidor e compartilhamento")
			}
		default:
			return errors.New("use caminho absoluto do Windows ou variável padrão de perfil/programas")
		}
		if strings.Contains(path, ":") {
			return errors.New("caminho com fluxo alternativo não é permitido")
		}
		if s.Kind == "aplicativo" && !strings.HasSuffix(strings.ToLower(s.Target), ".exe") {
			return errors.New("atalho de aplicativo exige arquivo .exe, sem argumentos")
		}
	default:
		return errors.New("tipo do atalho inválido")
	}
	return nil
}

// SupportsOS impede que caminhos de outro sistema sejam entregues ao usuário.
func (s StandardShortcut) SupportsOS(platform string) bool {
	if s.OS != "" && s.OS != platform {
		return false
	}
	if s.Kind == "site" {
		return true
	}
	unix := strings.HasPrefix(s.Target, "/") || strings.HasPrefix(s.Target, "~/")
	return unix == (platform == "linux" || platform == "darwin")
}

func invalidStandardText(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar
	}) >= 0
}

func (s StandardShortcut) Fingerprint() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type StandardAppState struct {
	App, Name, Target, State, Detail string
	JobID                            int64
}

func (s StandardAppState) Label() string {
	switch s.State {
	case "em_dia":
		return "em dia"
	case "agendado":
		return "agendado"
	case "em_andamento":
		return "em andamento"
	case "aguardando":
		return "aguardando fila"
	case "falhou":
		return "tentativa sem sucesso"
	default:
		return "bloqueado"
	}
}

type StandardizationPlan struct {
	Revision    string             `json:"revision"`
	Policies    []string           `json:"policies"`
	Shortcuts   []StandardShortcut `json:"shortcuts"`
	Apps        []StandardAppState `json:"apps"`
	Warnings    []string           `json:"warnings,omitempty"`
	CreatedJobs int                `json:"created_jobs,omitempty"`
	GeneratedAt time.Time          `json:"generated_at"`
}

func (p StandardizationPlan) Check() error {
	if len(p.Shortcuts) > MaxStandardizationShortcuts || len(p.Apps) > MaxStandardizationApps || len(p.Policies) > 100 || len(p.Revision) != 64 {
		return errors.New("plano de padronização fora dos limites")
	}
	if _, err := hex.DecodeString(p.Revision); err != nil {
		return errors.New("revisão inválida")
	}
	seen := map[string]bool{}
	for _, s := range p.Shortcuts {
		if err := s.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(s.Name)
		if seen[key] {
			return fmt.Errorf("nome de atalho repetido: %s", s.Name)
		}
		seen[key] = true
	}
	return nil
}

type StandardShortcutResult struct {
	Name   string `json:"name"`
	State  string `json:"state"` // em_dia, aplicado, removido ou conflito
	Detail string `json:"detail,omitempty"`
}

type StandardizationResult struct {
	Revision string                   `json:"revision"`
	Items    []StandardShortcutResult `json:"items"`
}

func (r StandardizationResult) Check() error {
	if len(r.Revision) != 64 || len(r.Items) > 2*MaxStandardizationShortcuts {
		return errors.New("resultado de padronização fora dos limites")
	}
	if _, err := hex.DecodeString(r.Revision); err != nil {
		return errors.New("revisão inválida")
	}
	for _, item := range r.Items {
		if item.Name == "" || len([]rune(item.Name)) > 64 || len([]rune(item.Detail)) > 200 || invalidStandardText(item.Name) || invalidStandardText(item.Detail) {
			return errors.New("resultado de atalho inválido")
		}
		switch item.State {
		case "em_dia", "aplicado", "removido", "conflito":
		default:
			return errors.New("estado de atalho inválido")
		}
	}
	return nil
}

type StandardizationReport struct {
	SelfServiceUser
	StandardizationResult
}
