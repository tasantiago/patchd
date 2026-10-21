package inventory

import (
	"context"
	"errors"
	"os"
	"regexp"

	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// Namespaces e consultas de ligar pela rede (Aula 22.1; o que cada uma diz está em
// windows_wake.go).
const (
	wmiDellBIOSNamespace = `root\dcim\sysman\biosattributes`
	wmiNetNamespace      = `root\StandardCimv2`
	wmiBitLockerNS       = `root\cimv2\Security\MicrosoftVolumeEncryption`
)

// Os tipos de protetor do BitLocker que pedem PIN na partida: TPM e PIN (4), TPM, chave de
// inicialização e PIN (6).
var bitlockerPINTypes = []int32{4, 6}

var driveLetter = regexp.MustCompile(`^[A-Za-z]:$`)

// collectWake lê o que decide o WoL. vendor: a interface do fabricante (vazio: o firmware não
// é lido). Falha de uma consulta vira erro parcial; classe que não existe (modelo sem a
// interface, Windows sem BitLocker), "não se sabe", sem erro.
func collectWake(ctx context.Context, vendor string) (*protocol.Wake, []error) {
	w := wmiWake{Vendor: vendor}
	var errs []error
	keep := func(err error) {
		if err != nil && !wincom.Missing(err) {
			errs = append(errs, err)
		}
	}
	err := wincom.Do(ctx, func() error {
		var rows []wincom.Row
		var err error
		switch vendor {
		case protocol.FirmwareHP:
			rows, err = wincom.Query(wmiHPNamespace, "SELECT Name, CurrentValue FROM HP_BIOSEnumeration", []string{"Name", "CurrentValue"})
		case protocol.FirmwareDell:
			rows, err = wincom.Query(wmiDellBIOSNamespace, "SELECT AttributeName, CurrentValue FROM EnumerationAttribute",
				[]string{"AttributeName", "CurrentValue"})
		case protocol.FirmwareLenovo:
			rows, err = wincom.Query(wmiLenovoNamespace, "SELECT CurrentSetting FROM Lenovo_BiosSetting", []string{"CurrentSetting"})
		}
		keep(err)
		for _, r := range rows {
			w.BIOSRows = append(w.BIOSRows, r)
		}

		nets, err := wincom.Query(wmiNetNamespace,
			"SELECT Name, MacAddress, NdisPhysicalMedium, HardwareInterface, Virtual, PnPDeviceID FROM MSFT_NetAdapter",
			[]string{"Name", "MacAddress", "NdisPhysicalMedium", "HardwareInterface", "Virtual", "PnPDeviceID"})
		keep(err)
		if err == nil {
			w.Adapters = []map[string]any{}
			for _, r := range nets {
				w.Adapters = append(w.Adapters, r)
			}
			pm, err := wincom.Query(wmiNetNamespace, "SELECT Name, WakeOnMagicPacket FROM MSFT_NetAdapterPowerManagementSettingData",
				[]string{"Name", "WakeOnMagicPacket"})
			keep(err)
			for _, r := range pm {
				w.Power = append(w.Power, r)
			}
			dw, err := wincom.Query(`root\WMI`, "SELECT InstanceName, Enable FROM MSPower_DeviceWakeEnable", []string{"InstanceName", "Enable"})
			keep(err)
			for _, r := range dw {
				w.DeviceWake = append(w.DeviceWake, r)
			}
		}
		w.BitLockerPIN = bitlockerPIN(keep)
		return nil
	})
	if err != nil {
		return nil, []error{err}
	}
	w.FastStartup = fastStartup()
	return windowsWakeInfo(w), errs
}

// bitlockerPIN: a unidade do sistema tem protetor com PIN. Sem BitLocker (namespace ou
// unidade ausente): falso. Deve rodar dentro do wincom.Do.
func bitlockerPIN(keep func(error)) *bool {
	drive := os.Getenv("SystemDrive")
	if !driveLetter.MatchString(drive) {
		return nil
	}
	for _, typ := range bitlockerPINTypes {
		row, _, err := wincom.ExecMethod(wmiBitLockerNS, "SELECT * FROM Win32_EncryptableVolume WHERE DriveLetter = '"+drive+"'",
			"GetKeyProtectors", []wincom.Param{{Name: "KeyProtectorType", Value: typ}}, []string{"VolumeKeyProtectorID"})
		switch {
		case errors.Is(err, wincom.ErrNoInstance) || wincom.Missing(err):
			return boolPtr(false)
		case err != nil:
			keep(err)
			return nil
		}
		if ids, ok := row["VolumeKeyProtectorID"].([]any); ok && len(ids) > 0 {
			return boolPtr(true)
		}
	}
	return boolPtr(false)
}

// fastStartup: HiberbootEnabled = 1 (a Inicialização Rápida do Windows).
func fastStartup() *bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Power`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("HiberbootEnabled")
	if err != nil {
		return nil
	}
	return boolPtr(v == 1)
}
