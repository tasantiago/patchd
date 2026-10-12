package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestConnectAvisaUmaVez: com o banco fora, o aviso sai uma vez, na primeira falha, e o
// Connect desiste depois da espera (Aula 9.1b). Não precisa de PostgreSQL: a porta 1 recusa.
func TestConnectAvisaUmaVez(t *testing.T) {
	avisos := 0
	var primeiro error
	inicio := time.Now()
	pool, err := ConnectNotify(context.Background(), "postgres://patchd@127.0.0.1:1/patchd?sslmode=disable&connect_timeout=1",
		1500*time.Millisecond, func(err error) {
			avisos++
			primeiro = err
		})
	if err == nil {
		pool.Close()
		t.Fatal("conectou numa porta fechada")
	}
	if avisos != 1 || primeiro == nil {
		t.Errorf("avisos = %d (quer 1), erro do aviso %v", avisos, primeiro)
	}
	if d := time.Since(inicio); d < 1500*time.Millisecond || d > 10*time.Second {
		t.Errorf("desistiu depois de %v (quer a espera de 1,5 s, com folga)", d)
	}
	if !strings.Contains(err.Error(), "indisponível após 1.5s") {
		t.Errorf("erro de espera vencida: %v", err)
	}
	// URL inválida: nem tenta, nem avisa.
	avisos = 0
	if _, err := ConnectNotify(context.Background(), "::nada::", time.Second, func(error) { avisos++ }); err == nil || avisos != 0 {
		t.Errorf("URL inválida: err %v, avisos %d", err, avisos)
	}
	// Contexto cancelado: desiste logo.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inicio = time.Now()
	if _, err := ConnectNotify(ctx, "postgres://patchd@127.0.0.1:1/patchd?sslmode=disable", time.Minute, nil); err == nil || !strings.Contains(err.Error(), "interrompida") || time.Since(inicio) > 5*time.Second {
		t.Errorf("contexto cancelado: %v depois de %v", err, time.Since(inicio))
	}
}
