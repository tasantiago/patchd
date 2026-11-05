package platform

import (
	"errors"

	"golang.org/x/sys/windows"
)

// WindowsInstallerBusy diz se há uma instalação do Windows Installer em execução agora. O
// serviço segura o mutex Global\_MSIExecute durante a execução de uma instalação MSI (de
// qualquer origem: outro job, o Windows Update instalando um MSI, alguém na máquina); uma
// segunda instalação fica esperando a vez, e um job nosso esperaria o prazo inteiro.
func WindowsInstallerBusy() (bool, error) {
	name, err := windows.UTF16PtrFromString(`Global\_MSIExecute`)
	if err != nil {
		return false, err
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	switch {
	case err == nil:
		_ = windows.CloseHandle(h)
		return true, nil
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return false, nil
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true, nil // existe, só não podemos abrir: está em uso do mesmo jeito
	}
	return false, err
}
