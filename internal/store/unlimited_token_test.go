package store

import (
	"context"
	"errors"
	"github.com/tasantiago/patchd/internal/identity"
	"testing"
	"time"
)

func testUnlimitedToken(t *testing.T, st contratoEnrollment) {
	ctx := context.Background()
	_, hash := identity.NewSecret(identity.EnrollmentPrefix)
	id, err := st.CreateEnrollmentToken(ctx, hash, "imagem", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		nm, _ := novaMaquina()
		if _, err := st.Enroll(ctx, hash, nm); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.EnrollmentTokens(ctx)
	if err != nil || list[0].Uses != 3 || list[0].Status(time.Now()) != "válido" {
		t.Fatal("unlimited token exhausted", err)
	}
	if _, err := st.RevokeEnrollmentToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	nm, _ := novaMaquina()
	if _, err := st.Enroll(ctx, hash, nm); !errors.Is(err, identity.ErrEnrollmentRejected) {
		t.Fatal("revocation failed", err)
	}
}
func TestUnlimitedTokenMemory(t *testing.T) { testUnlimitedToken(t, NewMemory()) }
func TestUnlimitedTokenPostgres(t *testing.T) {
	p := bancoDeTeste(t)
	if err := Migrate(context.Background(), p, silencioso()); err != nil {
		t.Fatal(err)
	}
	testUnlimitedToken(t, NewPostgres(p))
}
