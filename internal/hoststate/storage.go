package hoststate

import (
	"encoding/binary"
	"strings"
)

// Leitura do STORAGE_DEVICE_DESCRIPTOR do Windows (Aula 12.1d). Fica fora do arquivo do
// Windows para os testes rodarem em qualquer SO.

// le32 lê um uint32 little-endian em off; 0 se não couber.
func le32(b []byte, off int) uint32 {
	if off < 0 || off+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[off:])
}

// descString lê a string ASCII terminada em zero que começa em off (0 = ausente).
func descString(b []byte, off int) string {
	if off <= 0 || off >= len(b) {
		return ""
	}
	end := off
	for end < len(b) && b[end] != 0 {
		end++
	}
	s := strings.TrimSpace(string(b[off:end]))
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		return "" // lixo no descritor não vai para o painel
	}
	return s
}

// busName traduz o STORAGE_BUS_TYPE. Desconhecido: "".
func busName(t uint32) string {
	names := map[uint32]string{
		1: "SCSI", 2: "ATAPI", 3: "ATA", 4: "1394", 6: "Fibre Channel", 7: "USB", 8: "RAID",
		9: "iSCSI", 10: "SAS", 11: "SATA", 12: "SD", 13: "MMC", 14: "Virtual",
		15: "Virtual (arquivo)", 16: "Storage Spaces", 17: "NVMe", 18: "SCM", 19: "UFS",
	}
	return names[t]
}
