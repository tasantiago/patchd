package wincom

import (
	"errors"
	"testing"
)

func TestErrorMostraHResultEmHexa(t *testing.T) {
	base := errors.New("original")
	e := &Error{Op: "Search", HResult: 0x80244007, Err: base}
	if e.Error() != "Search: HRESULT 0x80244007" {
		t.Errorf("mensagem inesperada: %q", e.Error())
	}
	if !errors.Is(e, base) {
		t.Error("o erro original deveria continuar acessível")
	}
}
