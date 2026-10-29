package api

import (
	"context"
	"net"
	"strings"
	"syscall"
	"testing"
)

func TestDiscoveryTCPDiagnostics(t *testing.T) {
	s := &scanState{}
	s.begin("admin", 1)
	s.connectionError(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
	s.connectionError(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH})
	s.connectionError(context.DeadlineExceeded)
	s.connectionError(context.Canceled)
	got := s.diagnostics()
	for _, want := range []string{"1 sem resposta no prazo", "1 recusadas", "1 sem rota", "0 outros erros"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	s.end()
	s.begin("admin", 1)
	if s.diagnostics() != "" {
		t.Fatal("old diagnostics leaked into new scan")
	}
}
