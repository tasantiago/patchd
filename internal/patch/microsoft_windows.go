package patch

import (
	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/protocol"
)

// wuPolicyKey: as políticas do Windows Update (GPO).
const wuPolicyKey = `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`

// MicrosoftInfo diz se a fonte Microsoft (a reserva) serve nesta máquina. Duas políticas a
// fecham: "Não se conectar a nenhum local do Windows Update na Internet" (comum com WSUS) e
// "Remover o acesso a todos os recursos do Windows Update"; com qualquer uma ligada, o WUA
// recusa a busca direta.
func MicrosoftInfo() *protocol.MicrosoftUpdate {
	info := microsoftPolicy()
	info.InJob = true // este agente refaz pela Microsoft no job da política (Fallback)
	return info
}

func microsoftPolicy() *protocol.MicrosoftUpdate {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, wuPolicyKey, registry.QUERY_VALUE)
	if err != nil {
		return &protocol.MicrosoftUpdate{Available: true} // sem política nenhuma do Windows Update
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("DoNotConnectToWindowsUpdateInternetLocations"); err == nil && v == 1 {
		return &protocol.MicrosoftUpdate{Reason: "GPO \"Não se conectar a nenhum local do Windows Update na Internet\" ligada"}
	}
	if v, _, err := k.GetIntegerValue("DisableWindowsUpdateAccess"); err == nil && v == 1 {
		return &protocol.MicrosoftUpdate{Reason: "GPO \"Remover o acesso a todos os recursos do Windows Update\" ligada"}
	}
	return &protocol.MicrosoftUpdate{Available: true}
}
