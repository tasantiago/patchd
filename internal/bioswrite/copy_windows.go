package bioswrite

import (
	"errors"
	"time"

	"golang.org/x/sys/windows/registry"
)

// A cópia de emergência no Registro (docs/bios-senha.md, seção 9). Fica em HKLM, que só
// administrador e SYSTEM alteram; ler não é problema, porque é o envelope da guarda e só a
// chave privada de guarda (fora do servidor e fora da máquina) o abre.
const copyRegKey = `SOFTWARE\patchd\BIOS`

// Os valores: Copia é a senha que vale; CopiaIncerta, a de uma gravação sem resposta (a
// próxima gravação confirmada apaga).
const (
	regCopy          = "Copia"
	regCopyUncertain = "CopiaIncerta"
	regHowTo         = "ComoRevelar"
	howTo            = "Fora do patchd, com a chave de guarda: patchd-server bios reveal -key CHAVE -copia <valor de Copia> (docs/bios-senha.md, seção 9)"
)

// SaveRegistryCopy grava a cópia.
func SaveRegistryCopy(copy string, uncertain bool) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, copyRegKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	name := regCopy
	if uncertain {
		name = regCopyUncertain
	}
	at := time.Now().UTC().Format(time.RFC3339)
	for _, v := range [][2]string{{name, copy}, {name + "Em", at}, {regHowTo, howTo}} {
		if err := k.SetStringValue(v[0], v[1]); err != nil {
			return err
		}
	}
	if !uncertain {
		for _, n := range []string{regCopyUncertain, regCopyUncertain + "Em"} {
			if err := k.DeleteValue(n); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
