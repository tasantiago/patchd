package store

import (
	"context"
	"strings"
	"testing"
)

type contratoAutoatendimento interface {
	SelfServiceOffers(ctx context.Context) ([]SelfServiceOffer, error)
	SetSelfServiceOffer(ctx context.Context, o SelfServiceOffer) error
	DeleteSelfServiceOffer(ctx context.Context, app string) (bool, error)
}

// Aula 17.1: as ofertas do autoatendimento gravam, trocam e saem.
func testarAutoatendimento(t *testing.T, st contratoAutoatendimento) {
	ctx := context.Background()
	for nome, o := range map[string]SelfServiceOffer{
		"nada liberado": {AppID: "7zip"},
		"grupo ruim":    {AppID: "7zip", CanUpdate: true, Groups: []string{"; drop"}},
		"sem programa":  {CanUpdate: true},
	} {
		if err := st.SetSelfServiceOffer(ctx, o); err == nil {
			t.Errorf("%s: deveria recusar", nome)
		}
	}
	if err := st.SetSelfServiceOffer(ctx, SelfServiceOffer{AppID: "7zip", CanUpdate: true, UpdatedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	dn := `EXEMPLO\GG-Software`
	if err := st.SetSelfServiceOffer(ctx, SelfServiceOffer{AppID: "x", CanUpdate: true, Groups: []string{"CN=GG-X,DC=exemplo,DC=local"}}); err == nil {
		t.Error("DN não é mais aceito (Aula 17.2)")
	}
	if err := st.SetSelfServiceOffer(ctx, SelfServiceOffer{AppID: "notepad-plus-plus", CanUpdate: true, CanInstall: true,
		Groups: []string{"GG-TI", dn}, UpdatedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	l, err := st.SelfServiceOffers(ctx)
	if err != nil || len(l) != 2 || l[0].AppID != "7zip" || len(l[0].Groups) != 0 || l[0].CanInstall ||
		strings.Join(l[1].Groups, "|") != "GG-TI|"+dn || !l[1].CanInstall || l[1].UpdatedAt.IsZero() {
		t.Fatalf("lista: %+v %v", l, err)
	}
	// Trocar: a mesma chave.
	if err := st.SetSelfServiceOffer(ctx, SelfServiceOffer{AppID: "7zip", CanInstall: true, UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.SelfServiceOffers(ctx); len(l) != 2 || l[0].CanUpdate || !l[0].CanInstall || l[0].UpdatedBy != "ana" {
		t.Errorf("troca: %+v", l)
	}
	if ok, _ := st.DeleteSelfServiceOffer(ctx, "7zip"); !ok {
		t.Error("remover")
	}
	if ok, _ := st.DeleteSelfServiceOffer(ctx, "7zip"); ok {
		t.Error("remover de novo")
	}
}

func TestAutoatendimentoMemoria(t *testing.T) { testarAutoatendimento(t, NewMemory()) }

func TestAutoatendimentoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarAutoatendimento(t, NewPostgres(pool))
}
