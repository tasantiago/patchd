package wincom

import (
	"errors"
	"fmt"
)

// Error é uma falha de chamada COM com o HRESULT real. A descrição do Windows não entra
// na mensagem: ela vem traduzida e, em DISP_E_EXCEPTION, é genérica ("Exceção").
type Error struct {
	Op      string // operação COM: propriedade, método ou objeto
	HResult uint32
	Err     error // erro original do go-ole
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: HRESULT 0x%08X", e.Op, e.HResult)
}

func (e *Error) Unwrap() error { return e.Err }

// Missing diz se o erro é de namespace ou classe WMI que não existe nesta máquina
// (WBEM_E_INVALID_NAMESPACE, WBEM_E_INVALID_CLASS, WBEM_E_NOT_FOUND): o modelo não tem a
// interface do fabricante, o que não é falha.
func Missing(err error) bool {
	var we *Error
	if !errors.As(err, &we) {
		return false
	}
	switch we.HResult {
	case 0x8004100E, 0x80041010, 0x80041002:
		return true
	}
	return false
}
