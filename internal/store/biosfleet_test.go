package store

import (
	"context"
	"testing"
	"time"
)

type contratoSenhaPadrao interface {
	BIOSFleetPasswords(ctx context.Context) ([]BIOSFleetPassword, error)
	SetBIOSFleetPassword(ctx context.Context, b BIOSFleetPassword) error
	DeleteBIOSFleetPassword(ctx context.Context, vendor string) (bool, error)
}

// A senha padrão (docs/bios-senha.md, seção 10): uma por fabricante; gravar de novo troca.
func testarSenhaPadrao(t *testing.T, st contratoSenhaPadrao) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if l, err := st.BIOSFleetPasswords(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazia: %v %v", l, err)
	}
	for _, b := range []BIOSFleetPassword{
		{Vendor: "lenovo", Escrow: []byte{1}, EscrowKeyID: "0123456789ab", SetAt: t0, SetBy: "thyago"},
		{Vendor: "dell", Escrow: []byte{2}, EscrowKeyID: "0123456789ab", SetAt: t0, SetBy: "thyago"},
		{Vendor: "lenovo", Escrow: []byte{3}, EscrowKeyID: "ba9876543210", SetAt: t0.Add(time.Hour), SetBy: "ana"},
	} {
		if err := st.SetBIOSFleetPassword(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	l, err := st.BIOSFleetPasswords(ctx)
	if err != nil || len(l) != 2 || l[0].Vendor != "dell" || l[1].Escrow[0] != 3 || l[1].SetBy != "ana" || l[1].EscrowKeyID != "ba9876543210" ||
		!l[1].SetAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v %v", l, err)
	}
	if err := st.SetBIOSFleetPassword(ctx, BIOSFleetPassword{Vendor: "asus", Escrow: []byte{1}, EscrowKeyID: "0123456789ab", SetAt: t0, SetBy: "x"}); err == nil {
		if _, ok := st.(*Postgres); ok {
			t.Error("fabricante fora da lista aceito pelo banco")
		}
	}
	if ok, err := st.DeleteBIOSFleetPassword(ctx, "dell"); err != nil || !ok {
		t.Fatalf("apagar: %v %v", ok, err)
	}
	if ok, _ := st.DeleteBIOSFleetPassword(ctx, "dell"); ok {
		t.Error("apagou duas vezes")
	}
}

func TestSenhaPadraoMemoria(t *testing.T) { testarSenhaPadrao(t, NewMemory()) }

func TestSenhaPadraoPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarSenhaPadrao(t, NewPostgres(pool))
}
