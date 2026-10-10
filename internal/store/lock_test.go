package store

import (
	"context"
	"testing"
)

func TestTravaConsultiva(t *testing.T) {
	pool := bancoDeTeste(t)
	pg := NewPostgres(pool)
	ctx := context.Background()
	rodou := 0
	ok, err := pg.WithAdvisoryLock(ctx, 42, func(ctx context.Context) error {
		rodou++
		// Outro "servidor" (outra sessão) tenta a mesma trava: não consegue, e não roda.
		ok2, err := pg.WithAdvisoryLock(ctx, 42, func(context.Context) error { rodou += 10; return nil })
		if ok2 || err != nil {
			t.Errorf("segunda sessão pegou a trava: %t %v", ok2, err)
		}
		return nil
	})
	if !ok || err != nil || rodou != 1 {
		t.Fatalf("primeira: %t %v %d", ok, err, rodou)
	}
	// Solta: a próxima vez consegue.
	if ok, _ := pg.WithAdvisoryLock(ctx, 42, func(context.Context) error { return nil }); !ok {
		t.Error("a trava não foi solta")
	}
}
