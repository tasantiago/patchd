package api

import (
	"context"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// ComplianceSource calcula o estado de compliance (RF-23). Fica fora deste pacote porque
// a avaliação usa o catálogo inteiro; o servidor passa a implementação em WithCompliance.
type ComplianceSource interface {
	Fleet(ctx context.Context) (protocol.ComplianceFleet, error)
	Detail(ctx context.Context, id string) (protocol.ComplianceDetail, bool, error)
}

// WithCompliance liga as rotas de compliance e as contagens no painel.
func WithCompliance(c ComplianceSource) Option {
	return func(a *api) { a.compliance = c }
}

// complianceTimeout limita a avaliação da frota inteira dentro de um pedido (o prazo de
// escrita do servidor é de 60 s).
const complianceTimeout = 45 * time.Second

func (a *api) complianceOff(w http.ResponseWriter) bool {
	if a.compliance == nil {
		writeError(w, http.StatusServiceUnavailable, "compliance_unavailable",
			"o estado de compliance exige o PostgreSQL com o catálogo")
		return true
	}
	return false
}

func (a *api) fleetCompliance(w http.ResponseWriter, r *http.Request) {
	if a.complianceOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	fleet, err := a.compliance.Fleet(ctx)
	if err != nil {
		a.internalError(w, r, "avaliar a frota", err)
		return
	}
	writeJSON(w, http.StatusOK, fleet)
}

func (a *api) machineCompliance(w http.ResponseWriter, r *http.Request) {
	id, ok := a.machineID(w, r)
	if !ok || a.complianceOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	cs, found, err := a.compliance.Detail(ctx, id)
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "máquina desconhecida")
		return
	}
	writeJSON(w, http.StatusOK, cs)
}
