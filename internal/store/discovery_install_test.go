package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type discoveryInstallStore interface {
	DiscoveryCredential(context.Context) (DiscoveryCredential, error)
	SetDiscoveryCredential(context.Context, DiscoveryCredential) error
	ClaimDiscoveryInstall(context.Context, DiscoveryInstall) (bool, error)
	SaveDiscoveryInstall(context.Context, DiscoveryInstall) error
	DiscoveryInstalls(context.Context) ([]DiscoveryInstall, error)
}

func testDiscoveryInstallStore(t *testing.T, st discoveryInstallStore) {
	t.Helper()
	ctx := context.Background()
	c := DiscoveryCredential{Username: "LAB\\deploy", Password: []byte("ciphertext"), Enabled: true, UpdatedBy: "admin"}
	if err := st.SetDiscoveryCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	read, err := st.DiscoveryCredential(ctx)
	if err != nil || read.Username != c.Username || string(read.Password) != "ciphertext" || !read.Enabled {
		t.Fatal("credential persistence failed", err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	d := DiscoveryInstall{IP: "192.0.2.10", State: "instalando", At: at, By: "admin"}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 8 {
		wg.Go(func() {
			ok, err := st.ClaimDiscoveryInstall(ctx, d)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent claims: %d", wins.Load())
	}
	d.State = "aguardando registro"
	if err := st.SaveDiscoveryInstall(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.At = at.Add(time.Hour)
	if ok, err := st.ClaimDiscoveryInstall(ctx, d); ok || err != nil {
		t.Fatal("retried before registration cooldown", err)
	}
	d.At = at.Add(25 * time.Hour)
	if ok, err := st.ClaimDiscoveryInstall(ctx, d); !ok || err != nil {
		t.Fatal("did not allow later retry", err)
	}
	rows, err := st.DiscoveryInstalls(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != "instalando" {
		t.Fatal("result persistence failed", err)
	}
}
func TestDiscoveryInstallMemory(t *testing.T) { testDiscoveryInstallStore(t, NewMemory()) }
func TestDiscoveryInstallPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testDiscoveryInstallStore(t, NewPostgres(pool))
}
