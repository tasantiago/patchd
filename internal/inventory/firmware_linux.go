package inventory

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O firmware no Linux (Aula 21.1), só leitura: o modo pelo /sys/firmware/efi, o Secure Boot
// pela variável EFI e a versão do TPM pelo /sys/class/tpm. As senhas da BIOS não têm uma
// interface comum no Linux: ficam sem informação.
const (
	efiDir         = "/sys/firmware/efi/"
	efiPlatform    = efiDir + "fw_platform_size" // existe em toda máquina iniciada por UEFI
	secureBootVar  = efiDir + "efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"
	tpmVersionFile = "/sys/class/tpm/tpm0/tpm_version_major"
	tpmDevice      = "/sys/class/tpm/tpm0/dev"
)

func (c *linuxCollector) firmware() *protocol.Firmware {
	var f protocol.Firmware
	if _, err := c.readFile(efiPlatform); err == nil {
		f.Mode = protocol.FirmwareUEFI
		// A variável: 4 bytes de atributos e 1 byte com o valor (1 = ligado).
		if b, err := c.readFile(secureBootVar); err == nil && len(b) >= 5 {
			on := b[4] == 1
			f.SecureBoot = &on
		}
	} else if _, err := c.readFile(dmiDir + "bios_vendor"); err == nil {
		// Há DMI e não há EFI: a máquina iniciou pelo BIOS legado.
		f.Mode = protocol.FirmwareLegacy
		off := false
		f.SecureBoot = &off
	}
	if b, err := c.readFile(tpmVersionFile); err == nil {
		switch strings.TrimSpace(string(b)) {
		case "2":
			f.TPM = "2.0"
		case "1":
			f.TPM = "1.2"
		}
	} else if _, err := c.readFile(tpmDevice); err != nil && f.Mode != "" {
		f.TPM = protocol.TPMNone
	}
	if f.Empty() {
		return nil
	}
	return &f
}
