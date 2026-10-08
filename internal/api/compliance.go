package api

import (
	"context"
	"errors"
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

// canceled diz se o erro é o cliente que desistiu do pedido (fechou ou recarregou a
// página). Não é falha do servidor: vai para o log como informação, e nada é respondido.
func (a *api) canceled(r *http.Request, err error) bool {
	if !errors.Is(err, context.Canceled) || r.Context().Err() == nil {
		return false
	}
	a.logger.Info("pedido cancelado pelo cliente", "path", r.URL.Path, "request_id", RequestID(r.Context()))
	return true
}

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
	if a.canceled(r, err) {
		return
	}
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
	if a.canceled(r, err) {
		return
	}
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
