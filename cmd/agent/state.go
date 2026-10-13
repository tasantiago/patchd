package main

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/hoststate"
)

// stateTimeout: a coleta de rede e discos leva milissegundos; o prazo só protege o
// check-in de um volume que não responde.
const stateTimeout = 30 * time.Second

// printState coleta rede e discos (o mesmo que vai em cada check-in) e escreve em JSON.
// Falha parcial não muda o código de saída: ela aparece em "errors", como no check-in.
func printState(ctx context.Context, w io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, stateTimeout)
	defer cancel()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(hoststate.Collect(ctx)); err != nil {
		return exitRuntime
	}
	return exitOK
}
