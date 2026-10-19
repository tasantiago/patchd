package store

import (
	"bytes"
	"context"
	"testing"
)

type contratoArquivos interface {
	PanelAsset(ctx context.Context, name string) (PanelAsset, bool, error)
	SetPanelAsset(ctx context.Context, a PanelAsset) error
	DeletePanelAsset(ctx context.Context, name string) (bool, error)
}

// Aula 19.1: o logotipo grava, troca e sai; o que a migração recusa, a memória também recusa.
func testarArquivos(t *testing.T, st contratoArquivos) {
	ctx := context.Background()
	if _, ok, err := st.PanelAsset(ctx, "logo"); ok || err != nil {
		t.Fatalf("vazio: %v %v", ok, err)
	}
	for nome, a := range map[string]PanelAsset{
		"nome ruim": {Name: "Logo!", ContentType: "image/png", Data: []byte{1}},
		"tipo ruim": {Name: "logo", ContentType: "image/svg+xml", Data: []byte("<svg/>")},
		"vazio":     {Name: "logo", ContentType: "image/png"},
		"grande":    {Name: "logo", ContentType: "image/png", Data: make([]byte, MaxPanelAsset+1)},
	} {
		if err := st.SetPanelAsset(ctx, a); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
	png := []byte("\x89PNG\r\n\x1a\nconteudo")
	if err := st.SetPanelAsset(ctx, PanelAsset{Name: "logo", ContentType: "image/png", Data: png, UpdatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	a, ok, err := st.PanelAsset(ctx, "logo")
	if err != nil || !ok || a.ContentType != "image/png" || !bytes.Equal(a.Data, png) || len(a.SHA256) != 64 || a.UpdatedBy != "admin" || a.UpdatedAt.IsZero() {
		t.Fatalf("gravado: %+v %v %v", a, ok, err)
	}
	jpg := []byte("\xff\xd8\xff\xe0outro")
	if err := st.SetPanelAsset(ctx, PanelAsset{Name: "logo", ContentType: "image/jpeg", Data: jpg, UpdatedBy: "outro"}); err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.PanelAsset(ctx, "logo")
	if b.ContentType != "image/jpeg" || !bytes.Equal(b.Data, jpg) || b.SHA256 == a.SHA256 || b.UpdatedBy != "outro" {
		t.Errorf("trocado: %+v", b)
	}
	if ok, err := st.DeletePanelAsset(ctx, "logo"); !ok || err != nil {
		t.Errorf("apagar: %v %v", ok, err)
	}
	if ok, _ := st.DeletePanelAsset(ctx, "logo"); ok {
		t.Error("apagar de novo: não havia")
	}
	if _, ok, _ := st.PanelAsset(ctx, "logo"); ok {
		t.Error("continua lá")
	}
}

func TestArquivosDoPainelMemoria(t *testing.T) { testarArquivos(t, NewMemory()) }

func TestArquivosDoPainelPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarArquivos(t, NewPostgres(pool))
}
