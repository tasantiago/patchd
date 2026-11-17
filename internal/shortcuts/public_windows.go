//go:build windows

package shortcuts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"golang.org/x/sys/windows"
)

// PublicDesktop: o gerenciador dos atalhos na área de trabalho de todos os usuários, com o
// controle na pasta de dados do agente.
func PublicDesktop(dataDir string) (*PublicManager, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_PublicDesktop, 0)
	if err != nil {
		return nil, err
	}
	return &PublicManager{Desktop: desktop, StatePath: filepath.Join(dataDir, "atalhos-publicos.json"),
		Expand: expandSystem, Write: writePublic}, nil
}

// expandSystem troca a variável do começo (a lista de protocol.DesktopShortcut) pelo valor
// da máquina; o caminho que começa pela unidade fica como está.
func expandSystem(path string) (string, error) {
	v := protocol.DesktopEnvVar(path)
	if v == "" {
		return filepath.Clean(path), nil
	}
	val := os.Getenv(v)
	if v == "Public" && val == "" {
		val = os.Getenv("PUBLIC")
	}
	if val == "" || !filepath.IsAbs(val) {
		return "", fmt.Errorf("a variável %%%s%% não existe nesta máquina", v)
	}
	rest := path[len(v)+2:] // %Var%
	if !strings.HasPrefix(rest, `\`) {
		return "", errors.New("caminho inválido")
	}
	return filepath.Clean(val + rest), nil
}

func writePublic(path string, s protocol.DesktopShortcut, target, icon string, index int) error {
	location := target + ",0"
	if icon != "" {
		location = fmt.Sprintf("%s,%d", icon, index)
	}
	return saveLink(path, target, filepath.Dir(target), s.Name, location)
}

// CentralLink: o atalho da Central de software (o executável target) na área de trabalho
// pública, com o escudo do Windows (o mesmo do ícone perto do relógio).
func CentralLink(target string) (AppLink, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_PublicDesktop, 0)
	if err != nil {
		return AppLink{}, err
	}
	icon := ""
	if root := os.Getenv("SystemRoot"); filepath.IsAbs(root) {
		icon = filepath.Join(root, "System32", "imageres.dll") + ",73"
	}
	return AppLink{Desktop: desktop, Target: target, Icon: icon, Read: readLink,
		Write: func(path, target, description, icon string) error {
			return saveLink(path, target, filepath.Dir(target), description, icon)
		}}, nil
}
