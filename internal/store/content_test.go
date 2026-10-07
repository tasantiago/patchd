package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
)

func TestConteudo(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	if _, ok, err := st.ContentFile(ctx, "wsusscn2"); ok || err != nil {
		t.Fatalf("antes de gravar: %t %v", ok, err)
	}
	f := wsusscan.File{Name: "wsusscn2-0123456789abcdef.cab", Size: 674424266, SHA256: "0123456789abcdef" + "00",
		ETag: `"0325ba4d352dd1:0"`, LastModified: time.Date(2026, 10, 3, 1, 8, 4, 0, time.UTC), Cabinet: 674414170}
	for i := 0; i < 2; i++ { // a segunda vez atualiza a mesma linha
		if err := st.SaveContentFile(ctx, "wsusscn2", f); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := st.ContentFile(ctx, "wsusscn2")
	if err != nil || !ok || got != f {
		t.Errorf("lido: %+v %t %v", got, ok, err)
	}
}
