package inventory

import (
	"context"
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// Hardware consulta cinco classes WMI dentro de uma única sessão COM. Classe que falhar
// vira erro parcial; as demais seguem. Aula 21.1: e o firmware (modo, Secure Boot, TPM e as
// senhas, pela interface do fabricante).
func (c *windowsCollector) Hardware(ctx context.Context) (Hardware, error) {
	var w wmiHardware
	var fw wmiFirmware // Aula 21.1
	var errs []error

	err := wincom.Do(ctx, func() error {
		query := func(class string, props ...string) []wincom.Row {
			wql := "SELECT " + strings.Join(props, ", ") + " FROM " + class
			rows, err := wincom.Query(`root\cimv2`, wql, props)
			if err != nil {
				errs = append(errs, err)
			}
			return rows
		}
		if r := query("Win32_ComputerSystemProduct", "Vendor", "Name", "IdentifyingNumber", "UUID"); len(r) > 0 {
			w.Product = r[0]
		}
		if r := query("Win32_BIOS", "Manufacturer", "SMBIOSBIOSVersion", "ReleaseDate"); len(r) > 0 {
			w.BIOS = r[0]
		}
		if r := query("Win32_SystemEnclosure", "ChassisTypes"); len(r) > 0 {
			w.Enclosure = r[0]
		}
		if r := query("Win32_ComputerSystem", "TotalPhysicalMemory"); len(r) > 0 {
			w.System = r[0]
		}
		for _, r := range query("Win32_Processor", "Name", "NumberOfLogicalProcessors") {
			w.Processors = append(w.Processors, r)
		}
		// Aula 21.1: o TPM e as senhas do firmware, pela interface do fabricante.
		if rows, err := wincom.Query(wmiTPMNamespace, "SELECT SpecVersion FROM Win32_Tpm", []string{"SpecVersion"}); err == nil {
			fw.TPMQueried = true
			if len(rows) > 0 {
				fw.TPM = rows[0]
			}
		} else if !wmiMissing(err) {
			errs = append(errs, err)
		}
		vendor := firmwareInterface(asString(w.Product["Vendor"]))
		var rows []wincom.Row
		var err error
		switch vendor {
		case protocol.FirmwareHP:
			rows, err = wincom.Query(wmiHPNamespace, "SELECT Name, IsSet FROM HP_BIOSPassword", []string{"Name", "IsSet"})
			if err == nil { // Aula 21.5: o HP Sure Admin, que impede a troca de senha pelo patchd
				sa, saErr := wincom.Query(wmiHPNamespace, "SELECT Name, CurrentValue FROM HP_BIOSEnumeration WHERE Name = 'Enhanced BIOS Authentication Mode'",
					[]string{"Name", "CurrentValue"})
				switch {
				case saErr == nil:
					fw.SureAdminQueried = true
					if len(sa) > 0 {
						fw.SureAdmin = sa[0]
					}
				case !wmiMissing(saErr):
					errs = append(errs, saErr)
				}
			}
		case protocol.FirmwareDell:
			rows, err = wincom.Query(wmiDellNamespace, "SELECT NameId, IsPasswordSet FROM PasswordObject", []string{"NameId", "IsPasswordSet"})
		case protocol.FirmwareLenovo:
			rows, err = wincom.Query(wmiLenovoNamespace, "SELECT PasswordState FROM Lenovo_BiosPasswordSettings", []string{"PasswordState"})
		}
		for _, r := range rows {
			fw.Passwords = append(fw.Passwords, r)
		}
		if vendor == protocol.FirmwareLenovo && len(rows) > 0 {
			fw.LenovoState = rows[0]
		}
		switch {
		case vendor == "":
		case err == nil:
			fw.Interface = vendor
		case !wmiMissing(err): // modelo sem a interface: sem informação, sem erro
			errs = append(errs, err)
		}
		return nil
	})
	if err != nil {
		// COM não inicializou ou o prazo acabou: w e errs podem ainda estar em uso pela
		// goroutine do COM, então não são lidos.
		return Hardware{}, err
	}
	fw.FirmwareType = firmwareType()
	fw.SecureBoot = secureBootEnabled()
	hw := windowsHardwareInfo(w)
	if hw.Firmware = windowsFirmwareInfo(fw); hw.Firmware != nil {
		hw.Sources = append(hw.Sources, "GetFirmwareType", `HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State`)
		if fw.TPMQueried {
			hw.Sources = append(hw.Sources, `WMI `+wmiTPMNamespace+` Win32_Tpm`)
		}
		if hw.Firmware.Interface != "" {
			hw.Sources = append(hw.Sources, firmwareSource[hw.Firmware.Interface])
		}
	}
	return hw, errors.Join(errs...)
}

// Namespaces e consultas WMI do firmware.
const (
	wmiTPMNamespace    = `root\cimv2\Security\MicrosoftTpm`
	wmiHPNamespace     = `root\HP\InstrumentedBIOS`
	wmiDellNamespace   = `root\dcim\sysman\wmisecurity`
	wmiLenovoNamespace = `root\WMI`
)

// firmwareSource: de onde vieram as senhas, por fabricante.
var firmwareSource = map[string]string{
	protocol.FirmwareHP:     `WMI ` + wmiHPNamespace + ` HP_BIOSPassword`,
	protocol.FirmwareDell:   `WMI ` + wmiDellNamespace + ` PasswordObject`,
	protocol.FirmwareLenovo: `WMI ` + wmiLenovoNamespace + ` Lenovo_BiosPasswordSettings`,
}

// wmiMissing: o namespace ou a classe não existe nesta máquina (wincom.Missing).
func wmiMissing(err error) bool { return wincom.Missing(err) }

var procGetFirmwareType = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFirmwareType")

// firmwareType: 1 BIOS legado, 2 UEFI (GetFirmwareType, Windows 8 em diante); 0 não se sabe.
func firmwareType() uint32 {
	if procGetFirmwareType.Find() != nil {
		return 0
	}
	var t uint32
	if r, _, _ := procGetFirmwareType.Call(uintptr(unsafe.Pointer(&t))); r == 0 {
		return 0
	}
	if t == 1 || t == 2 {
		return t
	}
	return 0
}

// secureBootEnabled: o valor que o Windows grava na partida; sem a chave, não se sabe.
func secureBootEnabled() *bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\SecureBoot\State`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("UEFISecureBootEnabled")
	if err != nil {
		return nil
	}
	return boolPtr(v != 0)
}
