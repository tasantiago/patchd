package store

import (
	"context"
	"strings"
	"testing"
)

type contratoParametros interface {
	Settings(ctx context.Context) ([]Setting, error)
	SetSettings(ctx context.Context, list []Setting) error
	DeleteSetting(ctx context.Context, key string) (bool, error)
}

// Aula 19.2: os parâmetros da tela gravam juntos (ou nenhum), trocam e saem.
func testarParametros(t *testing.T, st contratoParametros) {
	ctx := context.Background()
	if l, err := st.Settings(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	for nome, l := range map[string][]Setting{
		"chave ruim":          {{Key: "OUTRA_COISA", Value: "x"}},
		"quebra":              {{Key: "PATCHD_TIMEZONE", Value: "a\nb"}},
		"grande":              {{Key: "PATCHD_PANEL_TITLE", Value: strings.Repeat("x", MaxSettingValue+1)}},
		"uma boa, outra ruim": {{Key: "PATCHD_PANEL_TITLE", Value: "ok"}, {Key: "patchd_x", Value: "y"}},
	} {
		if err := st.SetSettings(ctx, l); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
	if l, _ := st.Settings(ctx); len(l) != 0 {
		t.Fatal("recusado gravou alguma coisa")
	}
	if err := st.SetSettings(ctx, []Setting{{Key: "PATCHD_TIMEZONE", Value: "America/Porto_Velho", UpdatedBy: "admin"},
		{Key: "PATCHD_PANEL_TITLE", Value: "patchd · TJ", UpdatedBy: "admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettings(ctx, []Setting{{Key: "PATCHD_TIMEZONE", Value: "America/Manaus", UpdatedBy: "outro"}}); err != nil {
		t.Fatal(err)
	}
	l, err := st.Settings(ctx)
	if err != nil || len(l) != 2 || l[0].Key != "PATCHD_PANEL_TITLE" || l[1].Value != "America/Manaus" || l[1].UpdatedBy != "outro" || l[1].UpdatedAt.IsZero() {
		t.Fatalf("gravados: %+v %v", l, err)
	}
	if ok, err := st.DeleteSetting(ctx, "PATCHD_TIMEZONE"); !ok || err != nil {
		t.Errorf("apagar: %v %v", ok, err)
	}
	if ok, _ := st.DeleteSetting(ctx, "PATCHD_TIMEZONE"); ok {
		t.Error("apagar de novo: não havia")
	}
	if l, _ := st.Settings(ctx); len(l) != 1 {
		t.Errorf("sobrou: %+v", l)
	}
}

func TestParametrosMemoria(t *testing.T) { testarParametros(t, NewMemory()) }

func TestParametrosPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarParametros(t, NewPostgres(pool))
}
