package bioslegacy

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Read lê a senha do marcador. nil, nil: não há marcador, ou ele está sem senha. O chamador
// zera os bytes depois de usar (a cópia que o Registro devolveu como texto, o Go não alcança).
func Read() ([]byte, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, Path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer k.Close()
	s, _, err := k.GetStringValue(Value)
	switch {
	case errors.Is(err, registry.ErrNotExist) || errors.Is(err, registry.ErrUnexpectedType):
		return nil, nil
	case err != nil:
		return nil, err
	case s == "":
		return nil, nil
	}
	return []byte(s), nil
}

// Present diz se o marcador tem uma senha que serve de senha atual.
func Present() bool {
	pw, err := Read()
	defer clear(pw)
	return err == nil && Usable(pw)
}

// Clear apaga do marcador os valores com senha (o em uso e o pendente de versões antigas). A
// chave fica: sem a senha, o .bat para antes de tocar na BIOS se alguém o rodar de novo.
// cleared: havia o que apagar. O histórico do .bat, que fica em outra chave, não é tocado.
func Clear() (cleared bool, err error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, Path, registry.SET_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	for _, name := range []string{Value, PendingValue} {
		switch err := k.DeleteValue(name); {
		case err == nil:
			cleared = true
		case !errors.Is(err, registry.ErrNotExist):
			return cleared, err
		}
	}
	return cleared, nil
}
