package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrNoAgent: o canal não existe (o agente está parado, ou é anterior à v0.17.0).
var ErrNoAgent = errors.New("o agente do patchd não está atendendo nesta máquina (serviço parado, ou versão anterior à v0.17.0)")

// maxResponseSize: a loja inteira cabe com folga.
const maxResponseSize = 1 << 20

// Call abre o canal, manda o pedido e lê a resposta. É o que o app do usuário (17.4) e o
// "patchd-agent selfservice" usam.
func Call(ctx context.Context, req Request) (Response, error) {
	c, err := dial(ctx)
	if err != nil {
		return Response{}, err
	}
	type result struct {
		r   Response
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := exchange(c, req)
		done <- result{r, err}
	}()
	select {
	case res := <-done:
		c.Close()
		return res.r, res.err
	case <-ctx.Done():
		c.Close()
		return Response{}, fmt.Errorf("o agente não respondeu a tempo: %w", ctx.Err())
	}
}

func exchange(rw io.ReadWriter, req Request) (Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := rw.Write(append(b, '\n')); err != nil {
		return Response{}, fmt.Errorf("envio ao agente: %w", err)
	}
	line, err := bufio.NewReader(io.LimitReader(rw, maxResponseSize)).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return Response{}, fmt.Errorf("resposta do agente: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("resposta do agente ilegível: %w", err)
	}
	return resp, nil
}
