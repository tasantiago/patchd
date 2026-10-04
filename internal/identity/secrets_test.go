package identity

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewSecret(t *testing.T) {
	a, ha := NewSecret(CredentialPrefix)
	b, _ := NewSecret(CredentialPrefix)
	if a == b || !strings.HasPrefix(a, CredentialPrefix) || len(a) != len(CredentialPrefix)+43 {
		t.Errorf("segredo fora do formato ou repetido: %q %q", a, b)
	}
	if !bytes.Equal(ha, Hash(a)) || len(ha) != 32 {
		t.Error("o hash devolvido deve ser o SHA-256 do texto")
	}
}

func TestNewMachineID(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	vistos := map[string]bool{}
	for range 1000 {
		id := NewMachineID()
		if !v4.MatchString(id) || vistos[id] {
			t.Fatalf("UUID v4 inválido ou repetido: %q", id)
		}
		vistos[id] = true
	}
}

func TestBearerToken(t *testing.T) {
	casos := map[string]string{
		"Bearer patchd_mac_abc":   "patchd_mac_abc",
		"bearer  patchd_mac_abc ": "patchd_mac_abc",
	}
	for h, quer := range casos {
		if got, ok := BearerToken(h); !ok || got != quer {
			t.Errorf("BearerToken(%q) = %q, %t", h, got, ok)
		}
	}
	for _, h := range []string{"", "Bearer", "Bearer ", "Basic abc", "patchd_mac_abc"} {
		if _, ok := BearerToken(h); ok {
			t.Errorf("BearerToken(%q) deveria falhar", h)
		}
	}
}

func TestEnrollmentTokenStatus(t *testing.T) {
	agora := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	base := EnrollmentToken{ExpiresAt: agora.Add(time.Hour), MaxUses: 2, Uses: 1}
	if base.Status(agora) != "válido" {
		t.Error("deveria ser válido")
	}
	exp := base
	exp.ExpiresAt = agora
	esg := base
	esg.Uses = 2
	rev := base
	rev.RevokedAt = &agora
	for quer, tok := range map[string]EnrollmentToken{"expirado": exp, "esgotado": esg, "revogado": rev} {
		if got := tok.Status(agora); got != quer {
			t.Errorf("esperado %s, veio %s", quer, got)
		}
	}
}
