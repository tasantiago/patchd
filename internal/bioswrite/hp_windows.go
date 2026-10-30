package bioswrite

import (
	"context"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// A interface da HP (hp.go).
const (
	hpNamespace        = `root\HP\InstrumentedBIOS`
	hpSetupPassword    = "Setup Password"
	hpSureAdminSetting = "Enhanced BIOS Authentication Mode"
)

// Detect devolve a interface do fabricante desta máquina (HP, Dell ou Lenovo; docs/bios-config.md,
// seção 8). Um modelo sem a interface WMI do fabricante passa aqui e falha na primeira
// consulta, antes de qualquer escrita.
func Detect(ctx context.Context) (Vendor, error) {
	v, err := detect(ctx)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// DetectSettings é o Detect do job bios_settings.
func DetectSettings(ctx context.Context) (SettingsVendor, error) {
	v, err := detect(ctx)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// vendorImpl: o que cada fabricante implementa.
type vendorImpl interface {
	Vendor
	SettingsVendor
}

func detect(ctx context.Context) (vendorImpl, error) {
	var vendor string
	err := wincom.Do(ctx, func() error {
		rows, err := wincom.Query(`root\cimv2`, "SELECT Vendor FROM Win32_ComputerSystemProduct", []string{"Vendor"})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			s, _ := rows[0]["Vendor"].(string)
			vendor = vendorOf(s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch vendor {
	case "hp":
		return hpVendor{}, nil
	case "dell":
		return dellVendor{}, nil
	case "lenovo":
		return lenovoVendor{}, nil
	}
	return nil, ErrNoVendor
}

type hpVendor struct{}

func (hpVendor) Name() string { return "hp" }

func (hpVendor) SureAdmin(ctx context.Context) (bool, error) {
	var on bool
	err := wincom.Do(ctx, func() error {
		rows, err := wincom.Query(hpNamespace, "SELECT Name, CurrentValue FROM HP_BIOSEnumeration WHERE Name = '"+hpSureAdminSetting+"'",
			[]string{"Name", "CurrentValue"})
		if err != nil {
			return fmt.Errorf("interface da HP: %w", err)
		}
		on = len(rows) > 0 && hpSureAdminOn(rows[0]["CurrentValue"])
		return nil
	})
	return on, err
}

func (hpVendor) SetAdminPassword(ctx context.Context, newPW, current []byte) (Reply, bool, error) {
	var (
		reply  Reply
		called bool
	)
	err := wincom.Do(ctx, func() error {
		row, c, err := wincom.ExecMethod(hpNamespace, "SELECT * FROM HP_BIOSSettingInterface", "SetBIOSSetting",
			[]wincom.Param{{Name: "Name", Value: hpSetupPassword}, {Name: "Value", Value: hpPassword(newPW)},
				{Name: "Password", Value: hpPassword(current)}}, []string{"Return"})
		called = c
		if err != nil {
			return err
		}
		code, err := asCode(row["Return"])
		reply = hpReply(code)
		return err
	})
	return reply, called, err
}

// Settings lê as configurações de escolha como estão agora (o job bios_settings confere
// contra elas, não contra o inventário do servidor).
func (hpVendor) Settings(ctx context.Context) ([]protocol.BIOSSetting, error) {
	var out []protocol.BIOSSetting
	err := wincom.Do(ctx, func() error {
		props := []string{"Name", "Path", "CurrentValue", "PossibleValues", "IsReadOnly"}
		rows, err := wincom.Query(hpNamespace, "SELECT "+strings.Join(props, ", ")+" FROM HP_BIOSEnumeration", props)
		if err != nil {
			return fmt.Errorf("interface da HP: %w", err)
		}
		list := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			list = append(list, r)
		}
		out = hpEnumSettings(list)
		return nil
	})
	return out, err
}

// SetSetting grava uma configuração (SetBIOSSetting) com a senha em uso (só o prefixo, se a
// BIOS estiver sem senha).
func (hpVendor) SetSetting(ctx context.Context, name, value string, password []byte) (Reply, bool, error) {
	var (
		reply  Reply
		called bool
	)
	err := wincom.Do(ctx, func() error {
		row, c, err := wincom.ExecMethod(hpNamespace, "SELECT * FROM HP_BIOSSettingInterface", "SetBIOSSetting",
			[]wincom.Param{{Name: "Name", Value: name}, {Name: "Value", Value: value}, {Name: "Password", Value: hpPassword(password)}},
			[]string{"Return"})
		called = c
		if err != nil {
			return err
		}
		code, err := asCode(row["Return"])
		reply = hpReply(code)
		return err
	})
	return reply, called, err
}

// Commit: a HP aplica cada SetBIOSSetting sozinho.
func (hpVendor) Commit(context.Context, []byte) (Reply, bool, error) {
	return Reply{Code: "-", OK: true}, false, nil
}
