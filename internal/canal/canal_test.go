package canal

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHub(t *testing.T) {
	h := New()
	ctx := context.Background()

	// Sem aviso: a espera termina no prazo, sem acordar.
	if woke, _ := h.Wait(ctx, "m1", 10*time.Millisecond); woke {
		t.Fatal("acordou sem aviso")
	}
	// Aviso durante a espera: acorda na hora, com o motivo.
	var wg sync.WaitGroup
	wg.Add(1)
	var woke bool
	var reason string
	go func() { defer wg.Done(); woke, reason = h.Wait(ctx, "m1", 5*time.Second) }()
	for !h.IsConnected("m1") {
		time.Sleep(time.Millisecond)
	}
	if h.Connected() != 1 {
		t.Errorf("conectadas: %d", h.Connected())
	}
	start := time.Now()
	h.Notify("m1", "job")
	wg.Wait()
	if !woke || reason != "job" || time.Since(start) > time.Second {
		t.Fatalf("aviso: %v %q", woke, reason)
	}
	// Aviso com a máquina fora da espera: vale para a próxima (dentro do prazo).
	h.Notify("m2", "job")
	if woke, _ := h.Wait(ctx, "m2", time.Second); !woke {
		t.Error("o aviso guardado se perdeu")
	}
	if woke, _ := h.Wait(ctx, "m2", 10*time.Millisecond); woke {
		t.Error("o aviso guardado vale uma vez só")
	}
	agora := time.Now()
	h.now = func() time.Time { return agora }
	h.Notify("m3", "job")
	h.now = func() time.Time { return agora.Add(3 * time.Minute) }
	if woke, _ := h.Wait(ctx, "m3", 10*time.Millisecond); woke {
		t.Error("aviso vencido acordou")
	}
	h.now = time.Now

	// Segunda espera da mesma máquina: a primeira sai sem aviso.
	wg.Add(1)
	go func() { defer wg.Done(); woke, _ = h.Wait(ctx, "m4", 5*time.Second) }()
	for !h.IsConnected("m4") {
		time.Sleep(time.Millisecond)
	}
	done := make(chan bool, 1)
	go func() { w, _ := h.Wait(ctx, "m4", 5*time.Second); done <- w }()
	wg.Wait()
	if woke {
		t.Error("a espera substituída acordou com aviso")
	}
	for !h.IsConnected("m4") {
		time.Sleep(time.Millisecond)
	}
	// Encerrando: solta todas, e esperas novas voltam na hora.
	h.Close()
	if <-done {
		t.Error("encerrando, sem aviso")
	}
	if woke, _ := h.Wait(ctx, "m5", 5*time.Second); woke || h.Connected() != 0 {
		t.Error("depois de fechar")
	}
}
