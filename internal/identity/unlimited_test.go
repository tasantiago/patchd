package identity

import (
	"testing"
	"time"
)

func TestUnlimitedEnrollmentToken(t *testing.T) {
	now := time.Now()
	token := EnrollmentToken{MaxUses: 0, Uses: 1000000}
	if token.Status(now) != "válido" {
		t.Fatal("unlimited token exhausted")
	}
	token.RevokedAt = &now
	if token.Status(now) != "revogado" {
		t.Fatal("unlimited token cannot be revoked")
	}
	token.RevokedAt = nil
	token.ExpiresAt = now.Add(-time.Second)
	if token.Status(now) != "expirado" {
		t.Fatal("unlimited uses bypassed expiry")
	}
}
