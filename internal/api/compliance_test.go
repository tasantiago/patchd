package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

type complianceFalso struct {
	fleet protocol.ComplianceFleet
	err   error
}

func (c *complianceFalso) Fleet(context.Context) (protocol.ComplianceFleet, error) {
	return c.fleet, c.err
}

func (c *complianceFalso) Machine(_ context.Context, id string) (protocol.ComplianceStatus, bool, error) {
	for _, m := range c.fleet.Machines {
		if m.MachineID == id {
			return m, true, c.err
		}
	}
	return protocol.ComplianceStatus{}, false, c.err
}

func TestRotasDeCompliance(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{fleet: protocol.ComplianceFleet{
		EvaluatedAt: agora,
		Counts:      map[string]int{"faltando": 1, "reboot_pendente": 0, "sem_dado_recente": 0, "desconhecido": 0, "em_dia": 1},
		Machines: []protocol.ComplianceStatus{
			{MachineID: "7aa604f7-185d", State: "faltando", Reasons: []string{"1 pacotes com correção pendente"}, Pending: 1, PendingUnit: "pacotes", Exploited: 1, EvaluatedAt: agora},
			{MachineID: "287d1717-0001", State: "em_dia", Reasons: []string{}, EvaluatedAt: agora},
		},
	}}
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src))

	// Sem sessão: 401, como as outras rotas de leitura.
	for _, p := range []string{"/api/v1/compliance", "/api/v1/machines/7aa604f7-185d/compliance"} {
		if r := comCookie(h, "GET", p, nil); r.Code != http.StatusUnauthorized {
			t.Errorf("%s sem sessão: %d", p, r.Code)
		}
	}

	r := le(h, "/api/v1/compliance")
	var fleet protocol.ComplianceFleet
	if err := json.Unmarshal(r.Body.Bytes(), &fleet); err != nil || r.Code != http.StatusOK || len(fleet.Machines) != 2 || fleet.Counts["faltando"] != 1 {
		t.Fatalf("frota: %d %s", r.Code, r.Body)
	}
	if !strings.Contains(r.Body.String(), `"reasons":[]`) {
		t.Error("motivos vazios saem como lista vazia, não null")
	}

	r = le(h, "/api/v1/machines/7aa604f7-185d/compliance")
	var cs protocol.ComplianceStatus
	if err := json.Unmarshal(r.Body.Bytes(), &cs); err != nil || cs.State != "faltando" || cs.Exploited != 1 {
		t.Errorf("máquina: %d %s", r.Code, r.Body)
	}
	if r := le(h, "/api/v1/machines/nao-existe/compliance"); r.Code != http.StatusNotFound {
		t.Errorf("desconhecida: %d", r.Code)
	}
	if r := le(h, "/api/v1/machines/..%2Fx/compliance"); r.Code != http.StatusBadRequest && r.Code != http.StatusNotFound {
		t.Errorf("ID inválido: %d", r.Code)
	}

	// O painel mostra as contagens.
	if r := le(h, "/painel/"); !strings.Contains(r.Body.String(), "faltando: 1") || !strings.Contains(r.Body.String(), "em dia: 1") {
		t.Errorf("painel:\n%s", r.Body)
	}

	// Falha da avaliação: 500 na API, aviso no painel (que continua abrindo).
	src.err = errors.New("banco fora")
	if r := le(h, "/api/v1/compliance"); r.Code != http.StatusInternalServerError || strings.Contains(r.Body.String(), "banco fora") {
		t.Errorf("erro: %d %s", r.Code, r.Body)
	}
	if r := le(h, "/painel/"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "avaliação indisponível") {
		t.Errorf("painel com erro: %d", r.Code)
	}
}

func TestComplianceSemPostgres(t *testing.T) {
	h, _ := ambiente(t)
	if r := le(h, "/api/v1/compliance"); r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "PostgreSQL") {
		t.Errorf("sem fonte: %d %s", r.Code, r.Body)
	}
	if r := le(h, "/painel/"); strings.Contains(r.Body.String(), "Compliance") {
		t.Error("sem fonte, o painel não mostra a seção")
	}
}
