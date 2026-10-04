package agent

import (
	"errors"
	"io"
	"net"
	"strings"
)

// ClosedWithoutResponse reconhece a conexão aceita e encerrada sem resposta. O net/http
// relata isso de três jeitos, conforme o instante: EOF (o pedido já tinha sido escrito),
// "server closed idle connection" (o fechamento chegou antes; não há erro exportado, e a
// comparação pelo texto é a única disponível) e erro de leitura, como "connection reset by
// peer" (o outro lado fechou com dados não lidos). Recusa na conexão é erro de "dial", e
// esgotamento de prazo não conta: os dois têm causas e mensagens próprias.
func ClosedWithoutResponse(err error) bool {
	if err == nil {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "read" && !op.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "server closed idle connection")
}
