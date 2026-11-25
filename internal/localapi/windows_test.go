package localapi

import (
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestPedidoDasAtualizacoesDoWindows(t *testing.T) {
	agora := time.Now()
	if err := checkRequest(Request{Op: OpRequest, Action: protocol.SelfServiceWindows}); err != nil {
		t.Fatalf("windows sem programa: %v", err)
	}
	for nome, r := range map[string]Request{
		"com programa": {Op: OpRequest, Action: protocol.SelfServiceWindows, App: "7zip"},
		"com hora":     {Op: OpRequest, Action: protocol.SelfServiceWindows, At: &agora},
		"com fechar":   {Op: OpRequest, Action: protocol.SelfServiceWindows, Close: true},
	} {
		if checkRequest(r) == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}
