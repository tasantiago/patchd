package userapp

import (
	"context"
	"time"

	"golang.org/x/sys/windows"
)

// O pedido de abrir a página: o atalho da área de trabalho abre o patchd-app.exe; se a Central
// já está aberta na sessão (abriu no logon), a execução nova acha o mutex, avisa a aberta por
// um evento da sessão (Local\) e sai; a aberta mostra a página. O evento só pede para abrir a
// página da própria Central: não leva dado nenhum.
const openEventName = `Local\patchd-central-de-software-abrir`

// OpenRequests: os pedidos de abrir a página que chegam de outras execuções da Central na
// sessão, até o contexto acabar.
func OpenRequests(ctx context.Context) (<-chan struct{}, error) {
	name, _ := windows.UTF16PtrFromString(openEventName)
	h, err := windows.CreateEvent(nil, 0, 0, name) // reinício automático, começa desligado
	if h == 0 {
		return nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		defer windows.CloseHandle(h)
		for ctx.Err() == nil {
			switch ev, err := windows.WaitForSingleObject(h, 500); {
			case err != nil:
				return
			case ev == windows.WAIT_OBJECT_0:
				select {
				case ch <- struct{}{}:
				default: // um pedido já esperando basta
				}
			}
		}
	}()
	return ch, nil
}

// RequestOpen pede à Central aberta na sessão que mostre a página. Espera até wait pelo
// evento (a Central que acabou de abrir ainda pode estar criando o dela).
func RequestOpen(wait time.Duration) bool {
	name, _ := windows.UTF16PtrFromString(openEventName)
	deadline := time.Now().Add(wait)
	for {
		h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err == nil {
			err = windows.SetEvent(h)
			_ = windows.CloseHandle(h)
			return err == nil
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
