package userapp

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/shortcuts"
)

// WatchStandardization é independente da loja: atalhos funcionam mesmo sem ofertas de autoatendimento.
// O processo é o do usuário; nenhum atalho é gravado como SYSTEM no perfil dele.
func (a *App) WatchStandardization(ctx context.Context, log io.Writer) {
	manager, err := shortcuts.UserManager()
	if err != nil {
		return
	}
	wait := 15 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 5 * time.Minute
		c, cancel := context.WithTimeout(ctx, 90*time.Second)
		response, err := a.Call(c, localapi.Request{Op: localapi.OpStandardization})
		if err == nil && response.OK && response.Standardization != nil {
			result, applyErr := manager.Apply(*response.Standardization)
			if applyErr == nil {
				_, err = a.Call(c, localapi.Request{Op: localapi.OpStandardizationReport, StandardizationResult: &result})
			} else {
				err = applyErr
			}
			if err != nil && log != nil {
				fmt.Fprintf(log, "padronização: %v\n", err)
			}
		}
		cancel()
	}
}
