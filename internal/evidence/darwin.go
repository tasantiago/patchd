package evidence

import (
	"encoding/json"
	"fmt"
)

// parseHardwareIdentity lê o platform_UUID e o serial_number do
// "system_profiler SPHardwareDataType -json". Sem sufixo: testável no container.
func parseHardwareIdentity(data []byte) (uuid, serial string, err error) {
	var sp struct {
		Items []struct {
			PlatformUUID string `json:"platform_UUID"`
			SerialNumber string `json:"serial_number"`
		} `json:"SPHardwareDataType"`
	}
	if err := json.Unmarshal(data, &sp); err != nil {
		return "", "", fmt.Errorf("SPHardwareDataType: JSON inválido: %w", err)
	}
	if len(sp.Items) == 0 {
		return "", "", fmt.Errorf("SPHardwareDataType: sem itens")
	}
	return sp.Items[0].PlatformUUID, sp.Items[0].SerialNumber, nil
}
