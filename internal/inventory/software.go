package inventory

import (
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// uninstallEntry são os valores de uma subchave Uninstall do registro do Windows,
// já lidos. Fica fora do arquivo _windows.go para ser testável em qualquer SO.
type uninstallEntry struct {
	KeyName          string // nome da subchave; em instalações MSI é o ProductCode
	DisplayName      string
	DisplayVersion   string
	Publisher        string
	InstallDate      string // AAAAMMDD, quando o instalador segue o padrão
	ParentKeyName    string // preenchido em entradas que são atualização de outro produto
	ReleaseType      string
	SystemComponent  uint64 // 1 = oculto no Adicionar/Remover Programas
	WindowsInstaller uint64 // 1 = instalado via MSI
}

// uninstallLocation descreve a chave Uninstall de onde a entrada veio.
type uninstallLocation struct {
	Path  string // caminho completo da chave Uninstall, para o campo Source
	Scope string // "machine" ou "user"
	Arch  string // "x64" ou "x86" (visão do registro)
	User  string // SID, em instalações por usuário
}

// updateReleaseTypes são valores de ReleaseType usados por entradas de atualização.
var updateReleaseTypes = map[string]bool{
	"Security Update": true,
	"Update Rollup":   true,
	"Hotfix":          true,
	"Update":          true,
}

// softwareFromUninstall converte uma entrada Uninstall em Software.
// Entradas sem DisplayName são descartadas, como faz o Adicionar/Remover Programas.
func softwareFromUninstall(e uninstallEntry, loc uninstallLocation) (Software, bool) {
	name := strings.TrimSpace(e.DisplayName)
	if name == "" {
		return Software{}, false
	}
	s := Software{
		Name:            name,
		Version:         strings.TrimSpace(e.DisplayVersion),
		Publisher:       strings.TrimSpace(e.Publisher),
		InstallDate:     normalizeInstallDate(e.InstallDate),
		Scope:           loc.Scope,
		User:            loc.User,
		Arch:            loc.Arch,
		SystemComponent: e.SystemComponent == 1,
		IsUpdate:        strings.TrimSpace(e.ParentKeyName) != "" || updateReleaseTypes[strings.TrimSpace(e.ReleaseType)],
		Source:          loc.Path + `\` + e.KeyName,
	}
	if e.WindowsInstaller == 1 && isGUID(e.KeyName) {
		s.ProductCode = strings.ToUpper(e.KeyName)
	}
	return s, true
}

// normalizeInstallDate converte AAAAMMDD em AAAA-MM-DD. Qualquer outro valor vira vazio.
func normalizeInstallDate(s string) string {
	t, err := time.Parse("20060102", strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// isGUID reconhece o formato {XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX} (dígitos hexadecimais).
func isGUID(s string) bool {
	if len(s) != 38 || s[0] != '{' || s[37] != '}' {
		return false
	}
	for i := 1; i < 37; i++ {
		c := s[i]
		switch i {
		case 9, 14, 19, 24:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// isUserSID indica se uma subchave de HKEY_USERS é o perfil de um usuário:
// SIDs de contas (S-1-5-21-...), sem as chaves auxiliares terminadas em _Classes.
func isUserSID(name string) bool {
	return strings.HasPrefix(name, "S-1-5-21-") && !strings.HasSuffix(name, "_Classes")
}

// sortSoftware aplica a ordenação canônica do protocolo: uma única regra no projeto,
// a mesma que o hash do inventário usa.
func sortSoftware(list []Software) {
	protocol.SortSoftware(list)
}
