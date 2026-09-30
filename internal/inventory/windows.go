package inventory

import (
	"runtime"
	"strconv"
)

// Chave do registro com a versão do Windows (fonte de verdade definida na Aula 0.3).
const currentVersionKey = `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`

// Primeiro build do Windows 11. O ProductName do registro não serve para isso:
// continua dizendo "Windows 10" em máquinas Windows 11.
const firstWindows11Build = 22000

// currentVersion são os valores lidos de HKLM\...\Windows NT\CurrentVersion.
type currentVersion struct {
	CurrentBuild   string // ex.: "26200"
	UBR            uint64 // ex.: 9457; zero se ausente
	DisplayVersion string // ex.: "25H2"
	EditionID      string // ex.: "Professional"
}

// editionLabels traduz o EditionID para o rótulo comercial mais comum.
// Edições fora da lista aparecem com o EditionID cru.
var editionLabels = map[string]string{
	"Core":         "Home",
	"Professional": "Pro",
	"Enterprise":   "Enterprise",
	"Education":    "Education",
}

// windowsOSInfo monta o OSInfo a partir dos valores do registro. Fica fora do
// arquivo _windows.go para poder ser testada em qualquer SO.
func windowsOSInfo(cv currentVersion, hostname string) OSInfo {
	info := OSInfo{
		Family:   "windows",
		ID:       "windows",
		Edition:  cv.EditionID,
		Version:  cv.DisplayVersion,
		Arch:     runtime.GOARCH,
		Hostname: hostname,
		Sources:  []string{currentVersionKey},
	}

	info.Build = cv.CurrentBuild
	if cv.UBR > 0 {
		info.Build += "." + strconv.FormatUint(cv.UBR, 10)
	}

	name := "Windows"
	if b, err := strconv.Atoi(cv.CurrentBuild); err == nil {
		if b >= firstWindows11Build {
			name = "Windows 11"
		} else {
			name = "Windows 10"
		}
	}
	if label, ok := editionLabels[cv.EditionID]; ok {
		name += " " + label
	} else if cv.EditionID != "" {
		name += " " + cv.EditionID
	}
	info.Name = name
	return info
}
