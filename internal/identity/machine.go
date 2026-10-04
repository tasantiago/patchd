package identity

import (
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// NewMachine é o que o servidor grava ao registrar uma máquina.
type NewMachine struct {
	ID             string
	CredentialHash []byte
	AgentVersion   string
	Evidence       protocol.IdentityEvidence // como a máquina declarou (guardado cru)
	Keys           Keys                      // normalizadas (as que são comparadas)
}

// EnrollmentToken descreve um token de enrollment (nunca o segredo, que não é guardado).
type EnrollmentToken struct {
	ID        int64
	Note      string
	CreatedAt time.Time
	ExpiresAt time.Time
	MaxUses   int
	Uses      int
	RevokedAt *time.Time
}

// Status resume o token para exibição.
func (t EnrollmentToken) Status(now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revogado"
	case !now.Before(t.ExpiresAt):
		return "expirado"
	case t.Uses >= t.MaxUses:
		return "esgotado"
	default:
		return "válido"
	}
}
