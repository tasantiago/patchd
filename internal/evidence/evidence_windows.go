package evidence

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

const cryptographyKey = `SOFTWARE\Microsoft\Cryptography`

// collectOS: MachineGuid (gerado na instalação; o sysprep troca, uma imagem clonada sem
// sysprep repete) e o UUID e o serial do Win32_ComputerSystemProduct.
func collectOS(ctx context.Context, run platform.Runner) (protocol.IdentityEvidence, []string) {
	e := protocol.IdentityEvidence{OSFamily: "windows"}
	var problems []string

	// Visão de 64 bits: um processo de 32 bits leria a WOW6432Node, onde o valor não existe.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, cryptographyKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		problems = append(problems, "MachineGuid: "+err.Error())
	} else {
		v, _, err := k.GetStringValue("MachineGuid")
		k.Close()
		if err != nil {
			problems = append(problems, "MachineGuid: "+err.Error())
		}
		e.InstallID = v
	}

	err = wincom.Do(ctx, func() error {
		rows, err := wincom.Query(`root\cimv2`, "SELECT UUID, IdentifyingNumber FROM Win32_ComputerSystemProduct",
			[]string{"UUID", "IdentifyingNumber"})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("Win32_ComputerSystemProduct sem instâncias")
		}
		e.HardwareUUID, _ = rows[0]["UUID"].(string)
		e.Serial, _ = rows[0]["IdentifyingNumber"].(string)
		return nil
	})
	if err != nil {
		problems = append(problems, "Win32_ComputerSystemProduct: "+err.Error())
	}
	return e, problems
}
