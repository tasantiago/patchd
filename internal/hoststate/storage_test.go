package hoststate

import (
	"encoding/binary"
	"testing"
)

// Um STORAGE_DEVICE_DESCRIPTOR montado à mão: produto no offset 40, barramento NVMe.
func TestDescritor(t *testing.T) {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[16:], 40) // ProductIdOffset
	binary.LittleEndian.PutUint32(b[28:], 17) // BusType
	copy(b[40:], "SAMSUNG MZVL21T0  \x00lixo")
	if got := descString(b, int(le32(b, 16))); got != "SAMSUNG MZVL21T0" {
		t.Errorf("produto: %q", got)
	}
	if busName(le32(b, 28)) != "NVMe" || busName(11) != "SATA" || busName(99) != "" {
		t.Error("barramento")
	}
	if descString(b, 0) != "" || descString(b, 500) != "" || le32(b, 62) != 0 {
		t.Error("offset ausente ou fora do buffer")
	}
	b[41] = 0x01
	if descString(b, 40) != "" {
		t.Error("caractere de controle no descritor")
	}
}
