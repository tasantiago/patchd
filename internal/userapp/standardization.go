package userapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shortcuts"
)

// Intervalos da padronização no app do usuário.
const (
	stdFirstWait  = 15 * time.Second
	stdFirstJit   = 60 * time.Second // espalha o primeiro pedido de quem liga junto (as 6h da manhã)
	stdEvery      = 5 * time.Minute
	stdReportKeep = time.Hour // o relato igual ao anterior vai de novo só depois disso
)

// stdJitter: d com até ±20% de variação, para os apps de um parque não pedirem juntos.
func stdJitter(d time.Duration) time.Duration {
	return d - d/5 + time.Duration(rand.Int64N(int64(d/5)*2+1)) // #nosec G404 -- espalhar pedidos não pede aleatório criptográfico
}

// sameResult: o relato não mudou desde o último enviado.
func sameResult(a, b *protocol.StandardizationResult) bool {
	if a == nil || b == nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// WatchStandardization é independente da loja: atalhos funcionam mesmo sem ofertas de autoatendimento.
// O processo é o do usuário; nenhum atalho é gravado como SYSTEM no perfil dele. O relato só vai
// quando muda (ou de hora em hora): o servidor guarda o último, e o igual não lhe diz nada.
func (a *App) WatchStandardization(ctx context.Context, log io.Writer) {
	manager, err := shortcuts.UserManager()
	if err != nil {
		return
	}
	wait := stdFirstWait + time.Duration(rand.Int64N(int64(stdFirstJit))) // #nosec G404 -- espalhar pedidos
	var last *protocol.StandardizationResult
	var lastAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = stdJitter(stdEvery)
		c, cancel := context.WithTimeout(ctx, 90*time.Second)
		response, err := a.Call(c, localapi.Request{Op: localapi.OpStandardization})
		if err == nil && response.OK && response.Standardization != nil {
			result, applyErr := manager.Apply(*response.Standardization)
			switch {
			case applyErr != nil:
				err = applyErr
			case sameResult(&result, last) && time.Since(lastAt) < stdReportKeep:
				// nada mudou: sem relato
			default:
				_, err = a.Call(c, localapi.Request{Op: localapi.OpStandardizationReport, StandardizationResult: &result})
				if err == nil {
					r := result
					last, lastAt = &r, time.Now()
				}
			}
			if err != nil && log != nil {
				fmt.Fprintf(log, "padronização: %v\n", err)
			}
		}
		cancel()
	}
}
