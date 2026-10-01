package wincom

import (
	"errors"

	ole "github.com/go-ole/go-ole"
)

// dispException é o HRESULT DISP_E_EXCEPTION: a chamada IDispatch falhou, e o código
// real está no SCODE do EXCEPINFO.
const dispException = 0x80020009

// comError converte um erro do go-ole em *Error com o HRESULT real.
func comError(op string, err error) error {
	var oe *ole.OleError
	if !errors.As(err, &oe) {
		return &Error{Op: op, Err: err}
	}
	code := uint32(oe.Code())
	if code == dispException {
		// O EXCEPINFO vem como subcausa. A busca por interface evita depender do tipo exato.
		if s, ok := oe.SubError().(interface{ SCODE() uint32 }); ok && s.SCODE() != 0 {
			code = s.SCODE()
		}
	}
	return &Error{Op: op, HResult: code, Err: err}
}
