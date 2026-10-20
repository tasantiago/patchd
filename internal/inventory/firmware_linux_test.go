package inventory

import (
	"io/fs"
	"testing"
)

func TestFirmwareLinux(t *testing.T) {
	for nome, c := range map[string]struct {
		arquivos map[string]string
		quer     string
	}{
		"UEFI, Secure Boot ligado, TPM 2": {map[string]string{efiPlatform: "64\n", secureBootVar: "\x06\x00\x00\x00\x01", tpmVersionFile: "2\n"}, "uefi|s|2.0||?|?"},
		"UEFI, Secure Boot desligado":     {map[string]string{efiPlatform: "64\n", secureBootVar: "\x06\x00\x00\x00\x00"}, "uefi|n|nenhum||?|?"},
		"legado":                          {map[string]string{dmiDir + "bios_vendor": "SeaBIOS\n"}, "legado|n|nenhum||?|?"},
		"container (nada)":                {map[string]string{}, ""},
	} {
		col := &linuxCollector{readFile: arquivosComErros(c.arquivos, map[string]error{secureBootVar + "x": fs.ErrPermission})}
		f := col.firmware()
		got := ""
		if f != nil {
			got = txt(f)
			if err := f.Check(); err != nil {
				t.Errorf("%s: %v", nome, err)
			}
		}
		if got != c.quer {
			t.Errorf("%s: %q, quer %q", nome, got, c.quer)
		}
	}
}
