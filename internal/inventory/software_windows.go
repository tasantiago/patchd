package inventory

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// Caminhos das chaves Uninstall, relativos à raiz (HKLM ou HKU\<SID>).
const (
	machineUninstallPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	userUninstallPath    = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
)

// Software lê as chaves Uninstall nas visões de 64 e 32 bits do HKLM e nos perfis
// de usuário carregados em HKU. Em falha parcial, devolve o que leu junto com o erro.
func (c *windowsCollector) Software(ctx context.Context) ([]Software, error) {
	var list []Software
	var errs []error

	// Mesmo caminho, duas visões: a flag escolhe entre a visão nativa (64 bits) e a
	// redirecionada (32 bits, fisicamente em WOW6432Node). Source guarda o caminho físico.
	views := []struct {
		access uint32
		loc    uninstallLocation
	}{
		{registry.WOW64_64KEY, uninstallLocation{
			Path: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, Scope: "machine", Arch: "x64"}},
		{registry.WOW64_32KEY, uninstallLocation{
			Path: `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, Scope: "machine", Arch: "x86"}},
	}
	for _, v := range views {
		items, err := readUninstallKey(ctx, registry.LOCAL_MACHINE, machineUninstallPath, v.access, v.loc)
		list = append(list, items...)
		if err != nil {
			errs = append(errs, err)
		}
	}

	// Instalações por usuário: só perfis carregados (usuários com sessão aberta).
	sids, err := loadedUserSIDs()
	if err != nil {
		errs = append(errs, err)
	}
	for _, sid := range sids {
		loc := uninstallLocation{Path: `HKU\` + sid + `\` + userUninstallPath, Scope: "user", User: sid}
		items, err := readUninstallKey(ctx, registry.USERS, sid+`\`+userUninstallPath, registry.WOW64_64KEY, loc)
		list = append(list, items...)
		if err != nil {
			errs = append(errs, err)
		}
	}

	sortSoftware(list)
	return list, errors.Join(errs...)
}

// loadedUserSIDs lista os perfis de usuário carregados em HKEY_USERS.
func loadedUserSIDs() ([]string, error) {
	// n <= 0: todas as subchaves, como os.File.Readdirnames.
	names, err := registry.USERS.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("listar HKU: %w", err)
	}
	var sids []string
	for _, n := range names {
		if isUserSID(n) {
			sids = append(sids, n)
		}
	}
	return sids, nil
}

// readUninstallKey lê todas as subchaves de uma chave Uninstall.
// Chave inexistente não é erro; subchave ilegível vira erro sem interromper as demais.
func readUninstallKey(ctx context.Context, root registry.Key, path string, access uint32, loc uninstallLocation) ([]Software, error) {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|access)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("abrir %s: %w", loc.Path, err)
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("listar %s: %w", loc.Path, err)
	}

	var list []Software
	var errs []error
	for _, name := range names {
		// Respeita o prazo total da coleta.
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: coleta interrompida: %w", loc.Path, err))
			break
		}
		e, err := readUninstallEntry(k, name, access)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s\\%s: %w", loc.Path, name, err))
			continue
		}
		if s, ok := softwareFromUninstall(e, loc); ok {
			list = append(list, s)
		}
	}
	return list, errors.Join(errs...)
}

// readUninstallEntry lê os valores de uma subchave Uninstall. Valores ausentes ou de
// tipo inesperado ficam vazios: instaladores gravam de tudo nessas chaves.
func readUninstallEntry(parent registry.Key, name string, access uint32) (uninstallEntry, error) {
	e := uninstallEntry{KeyName: name}
	k, err := registry.OpenKey(parent, name, registry.QUERY_VALUE|access)
	if err != nil {
		return e, err
	}
	defer k.Close()

	e.DisplayName = stringValue(k, "DisplayName")
	e.DisplayVersion = stringValue(k, "DisplayVersion")
	e.Publisher = stringValue(k, "Publisher")
	e.InstallDate = stringValue(k, "InstallDate")
	e.ParentKeyName = stringValue(k, "ParentKeyName")
	e.ReleaseType = stringValue(k, "ReleaseType")
	e.SystemComponent = intValue(k, "SystemComponent")
	e.WindowsInstaller = intValue(k, "WindowsInstaller")
	return e, nil
}

// stringValue lê um valor de texto; ausente ou de outro tipo vira "".
func stringValue(k registry.Key, name string) string {
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

// intValue lê um DWORD/QWORD; ausente ou de outro tipo vira 0.
func intValue(k registry.Key, name string) uint64 {
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0
	}
	return v
}
