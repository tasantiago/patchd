package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestMaquinaAposentada(t *testing.T) {
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	abreSessao(t, st, sessaoTeste, time.Now().UTC())
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m := registra(t, h, tok)
	if r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inventario(t, "1")); r.Code != http.StatusCreated {
		t.Fatalf("inventário: %d", r.Code)
	}

	if ok, err := st.RetireMachine(context.Background(), m.MachineID, "registro antigo"); !ok || err != nil {
		t.Fatal(err)
	}
	// O agente da aposentada recebe 401; a lista da frota não a mostra.
	if r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inventario(t, "2")); r.Code != http.StatusUnauthorized {
		t.Errorf("envio da aposentada: %d", r.Code)
	}
	var lista []protocol.MachineSummary
	_ = json.Unmarshal(le(h, "/api/v1/machines").Body.Bytes(), &lista)
	if len(lista) != 0 {
		t.Errorf("a aposentada não aparece na lista: %+v", lista)
	}

	// Restaurada, volta a enviar.
	if ok, _ := st.RestoreMachine(context.Background(), m.MachineID); !ok {
		t.Fatal("restore")
	}
	if r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inventario(t, "2")); r.Code != http.StatusCreated {
		t.Errorf("envio depois do restore: %d", r.Code)
	}
}
