package wincom

import "fmt"

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
