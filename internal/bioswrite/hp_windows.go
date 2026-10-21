package bioswrite

import (
	"context"
	"fmt"
	"math"

	"github.com/tasantiago/patchd/internal/wincom"
)

// A interface da HP (hp.go).
const (
	hpNamespace        = `root\HP\InstrumentedBIOS`
	hpSetupPassword    = "Setup Password"
	hpSureAdminSetting = "Enhanced BIOS Authentication Mode"
)

// Detect devolve a interface do fabricante desta máquina (só a HP, por enquanto).
func Detect(ctx context.Context) (Vendor, error) {
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
	switch {
	case err != nil:
		return nil, err
	case vendor == "hp":
		return hpVendor{}, nil
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

func (hpVendor) SetAdminPassword(ctx context.Context, newPW, current []byte) (uint32, bool, error) {
	var (
		code   uint32
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
		code, err = asCode(row["Return"])
		return err
	})
	return code, called, err
}

// asCode: o Return do WMI vem como inteiro de algum tamanho.
func asCode(v any) (uint32, error) {
	var n int64
	switch x := v.(type) {
	case int32:
		n = int64(x)
	case uint32:
		n = int64(x)
	case int64:
		n = x
	case uint64:
		if x > math.MaxUint32 {
			return 0, fmt.Errorf("código de retorno fora da faixa: %d", x)
		}
		n = int64(x)
	case int16:
		n = int64(x)
	case uint16:
		n = int64(x)
	case uint8:
		n = int64(x)
	default:
		return 0, fmt.Errorf("código de retorno de tipo inesperado %T", v)
	}
	if n < 0 || n > math.MaxUint32 {
		return 0, fmt.Errorf("código de retorno fora da faixa: %d", n)
	}
	return uint32(n), nil
}
