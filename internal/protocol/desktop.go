package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// O atalho na área de trabalho de todos os usuários (docs/operacao.md, "Instaladores
// próprios e atalho"): a regra de um programa de terceiros pode pedir um atalho para o
// executável dele. O servidor manda a lista no check-in, e o agente do Windows, como SYSTEM,
// mantém os atalhos na área de trabalho pública (C:\Users\Public\Desktop): cria quando o
// executável existe (o programa foi instalado, pelo patchd ou não), troca quando a regra muda
// e apaga o que criou quando o executável some (o programa saiu) ou a regra deixa de pedir.
//
// O atalho só aponta para um .exe que já está no disco, sem argumentos: nada é executado nem
// baixado. Os caminhos são absolutos (C:\...) ou começam por uma variável de sistema da lista
// (%ProgramFiles%, %ProgramFiles(x86)%...), nunca de perfil: o agente roda como SYSTEM.

// DesktopShortcut é um atalho da lista.
type DesktopShortcut struct {
	App    string `json:"app"`            // a regra do programa (irpf-2026)
	Name   string `json:"name"`           // o nome do atalho, sem .lnk
	Target string `json:"target"`         // o executável
	Icon   string `json:"icon,omitempty"` // .ico, .exe ou .dll, com ",N" opcional; vazio: o do executável
}

// DesktopShortcuts é a lista no check-in. Ausente (servidor anterior): o agente mantém o
// que tem; presente e vazia: o agente apaga os atalhos que criou.
type DesktopShortcuts struct {
	Items []DesktopShortcut `json:"items"`
}

// MaxDesktopShortcuts: quantos atalhos a lista leva.
const MaxDesktopShortcuts = 200

const maxDesktopPath = 260

var (
	desktopApp   = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}$`)
	desktopDrive = regexp.MustCompile(`^[A-Za-z]:\\`)
	// As variáveis de sistema que valem para o SYSTEM e para todos os usuários.
	desktopEnv  = regexp.MustCompile(`(?i)^%(ProgramFiles|ProgramFiles\(x86\)|ProgramW6432|ProgramData|SystemDrive|SystemRoot|Public)%\\`)
	desktopIcon = regexp.MustCompile(`,([0-9]{1,4})$`)
)

// DesktopEnvVar: a variável do começo do caminho ("ProgramFiles(x86)"), ou "" se ele começa
// pela unidade.
func DesktopEnvVar(path string) string {
	m := desktopEnv.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1]
}

// checkDesktopPath confere um caminho do Windows com uma das extensões.
func checkDesktopPath(what, path string, exts ...string) error {
	switch {
	case path == "" || len(path) > maxDesktopPath || strings.TrimSpace(path) != path || invalidStandardText(path):
		return errors.New(what + ": um caminho de até 260 caracteres")
	case strings.ContainsAny(path, `/<>"|?*`):
		return errors.New(what + `: use \ e nenhum dos caracteres / < > " | ? *`)
	}
	rest := ""
	switch {
	case desktopDrive.MatchString(path):
		rest = path[3:]
	case desktopEnv.MatchString(path):
		rest = path[len(desktopEnv.FindString(path)):]
	default:
		return errors.New(what + `: comece pela unidade (C:\...) ou por %ProgramFiles%, %ProgramFiles(x86)%, %ProgramW6432%, %ProgramData%, %SystemDrive%, %SystemRoot% ou %Public%`)
	}
	if strings.ContainsAny(rest, ":%") {
		return errors.New(what + ": sem : nem % depois do começo do caminho")
	}
	for _, part := range strings.Split(rest, `\`) {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New(what + `: sem \\, sem . ou .. e sem pasta terminando em ponto ou espaço`)
		}
	}
	lower := strings.ToLower(path)
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return nil
		}
	}
	return errors.New(what + ": o arquivo tem de terminar em " + strings.Join(exts, ", "))
}

// SplitIcon separa o arquivo do índice ("C:\x\app.exe,2" → "C:\x\app.exe", 2).
func SplitIcon(icon string) (string, int) {
	m := desktopIcon.FindStringSubmatchIndex(icon)
	if m == nil {
		return icon, 0
	}
	n, _ := strconv.Atoi(icon[m[2]:m[3]])
	return icon[:m[0]], n
}

// ValidDesktopName: o nome do atalho é um nome de arquivo do Windows.
func ValidDesktopName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && len([]rune(name)) <= 64 && !strings.ContainsAny(name, `<>:"/\|?*`) &&
		!strings.HasSuffix(name, ".") && !shortcutReserved.MatchString(name) && !invalidStandardText(name)
}

// Validate confere o atalho.
func (s DesktopShortcut) Validate() error {
	if !desktopApp.MatchString(s.App) {
		return errors.New("programa do atalho inválido")
	}
	if !ValidDesktopName(s.Name) {
		return errors.New("nome do atalho: até 64 caracteres que valham num nome de arquivo do Windows (sem < > : \" / \\ | ? *)")
	}
	if err := checkDesktopPath("o executável do atalho", s.Target, ".exe"); err != nil {
		return err
	}
	if s.Icon != "" {
		file, _ := SplitIcon(s.Icon)
		if err := checkDesktopPath("o ícone do atalho", file, ".ico", ".exe", ".dll"); err != nil {
			return err
		}
	}
	return nil
}

// Fingerprint muda quando o atalho muda (o agente refaz o arquivo).
func (s DesktopShortcut) Fingerprint() string {
	h := sha256.Sum256([]byte(s.App + "\x00" + s.Name + "\x00" + s.Target + "\x00" + s.Icon))
	return hex.EncodeToString(h[:16])
}

// Check confere a lista: até MaxDesktopShortcuts, cada um válido, sem dois com o mesmo nome
// (o arquivo é o mesmo).
func (l DesktopShortcuts) Check() error {
	if len(l.Items) > MaxDesktopShortcuts {
		return errors.New("lista de atalhos grande demais")
	}
	seen := map[string]bool{}
	for _, s := range l.Items {
		if err := s.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(s.Name)
		if seen[key] {
			return errors.New("dois atalhos com o nome " + s.Name)
		}
		seen[key] = true
	}
	return nil
}
