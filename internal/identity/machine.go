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
	// ViaRelay: o pedido chegou pela porta só de agentes (o relé na nuvem). Só o token
	// "pelo relé" vale por ela, e ele não vale por outra (docs/rele.md).
	ViaRelay bool
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
	// Relay: token de registro pelo relé, de uso único, que só vale pela porta do relé.
	Relay bool
}

// RelayMismatch: o motivo da recusa quando o token e a porta não combinam ("" se combinam).
func (t EnrollmentToken) RelayMismatch(viaRelay bool) string {
	switch {
	case t.Relay && !viaRelay:
		return "é de registro pelo relé e só vale pela porta do relé"
	case !t.Relay && viaRelay:
		return "é da rede interna e não vale pelo relé"
	}
	return ""
}

// Status resume o token para exibição.
func (t EnrollmentToken) Status(now time.Time) string {
	switch {
	case t.RevokedAt != nil:
		return "revogado"
	case !t.ExpiresAt.IsZero() && !now.Before(t.ExpiresAt):
		return "expirado"
	case t.MaxUses > 0 && t.Uses >= t.MaxUses:
		return "esgotado"
	default:
		return "válido"
	}
}
