package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// ambiente monta a API sobre a memória, com um token de enrollment de 5 usos.
func ambiente(t *testing.T) (http.Handler, string) {
	t.Helper()
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	return api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), tok
}

func envia(h http.Handler, method, path, bearer string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func registra(t *testing.T, h http.Handler, tok string) protocol.EnrollResponse {
	t.Helper()
	corpo, _ := json.Marshal(protocol.EnrollRequest{
		AgentVersion: "v0.4.0-teste",
		Evidence:     protocol.IdentityEvidence{InstallID: "guid-1", Hostname: "itom-teste", OSFamily: "linux"},
	})
	r := envia(h, "POST", "/api/v1/enroll", tok, corpo)
	if r.Code != http.StatusCreated {
		t.Fatalf("enrollment: %d %s", r.Code, r.Body)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Error("a resposta com a credencial não pode ser guardada em cache")
	}
	var resp protocol.EnrollResponse
	if err := json.Unmarshal(r.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Credential, identity.CredentialPrefix) || len(resp.MachineID) != 36 {
		t.Fatalf("resposta do enrollment fora do formato: %+v", resp)
	}
	return resp
}

func inventario(t *testing.T, versao string) []byte {
	t.Helper()
	rep := protocol.InventoryReport{
		SchemaVersion: protocol.InventorySchemaVersion,
		AgentVersion:  "v0.4.0-teste",
		CollectedAt:   time.Now().UTC(),
		OS:            protocol.OSInfo{Family: "linux", ID: "ubuntu", Name: "Ubuntu", Version: "26.04", Hostname: "itom-teste"},
		Software:      []protocol.Software{{Name: "libcares2", Version: versao, Scope: "machine", Source: "dpkg"}},
	}
	h, err := rep.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	rep.Hash = h
	b, _ := json.Marshal(rep)
	return b
}

func TestFluxoCompleto(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)

	r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inventario(t, "1.34.6-1"))
	if r.Code != http.StatusCreated {
		t.Fatalf("inventário: %d %s", r.Code, r.Body)
	}
	r = envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inventario(t, "1.34.6-1"))
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"unchanged"`) {
		t.Errorf("mesmo conteúdo: %d %s", r.Code, r.Body)
	}

	sim := true
	scan, _ := json.Marshal(protocol.PatchScanReport{
		SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
		Reboot: &protocol.RebootStatus{Pending: &sim},
	})
	r = envia(h, "POST", "/api/v1/agent/scan", m.Credential, scan)
	if r.Code != http.StatusCreated || strings.Contains(r.Body.String(), `"hash"`) {
		t.Errorf("busca: %d %s (sem campo hash vazio)", r.Code, r.Body)
	}

	// O relatório ficou na máquina emitida no enrollment, e não em um ID escolhido pelo cliente.
	r = envia(h, "GET", "/api/v1/machines/"+m.MachineID+"/inventory", "", nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "libcares2") {
		t.Errorf("leitura pelo ID emitido: %d %s", r.Code, r.Body)
	}
	r = envia(h, "GET", "/api/v1/machines", "", nil)
	var lista []protocol.MachineSummary
	_ = json.Unmarshal(r.Body.Bytes(), &lista)
	if len(lista) != 1 || lista[0].ID != m.MachineID || lista[0].RebootPending == nil || !*lista[0].RebootPending {
		t.Errorf("lista: %+v", lista)
	}
}

func TestDuasMaquinasNaoSeMisturam(t *testing.T) {
	h, tok := ambiente(t)
	a, b := registra(t, h, tok), registra(t, h, tok)
	if a.MachineID == b.MachineID || a.Credential == b.Credential {
		t.Fatal("cada enrollment emite ID e credencial próprios")
	}
	envia(h, "POST", "/api/v1/agent/inventory", a.Credential, inventario(t, "A"))
	envia(h, "POST", "/api/v1/agent/inventory", b.Credential, inventario(t, "B"))
	ra := envia(h, "GET", "/api/v1/machines/"+a.MachineID+"/inventory", "", nil)
	rb := envia(h, "GET", "/api/v1/machines/"+b.MachineID+"/inventory", "", nil)
	if !strings.Contains(ra.Body.String(), `"version":"A"`) || !strings.Contains(rb.Body.String(), `"version":"B"`) {
		t.Errorf("cada credencial grava na própria máquina:\nA: %s\nB: %s", ra.Body, rb.Body)
	}
}

func TestRecusasDeAutenticacao(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)
	inv := inventario(t, "1")
	corpoEnroll, _ := json.Marshal(protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{OSFamily: "linux"}})

	casos := []struct {
		nome, caminho, bearer string
		corpo                 []byte
	}{
		{"envio sem credencial", "/api/v1/agent/inventory", "", inv},
		{"envio com credencial inventada", "/api/v1/agent/inventory", "patchd_mac_inventada", inv},
		{"envio com o token de enrollment", "/api/v1/agent/inventory", tok, inv},
		{"enrollment sem token", "/api/v1/enroll", "", corpoEnroll},
		{"enrollment com token inventado", "/api/v1/enroll", "patchd_enr_inventado", corpoEnroll},
		{"enrollment com a credencial da máquina", "/api/v1/enroll", m.Credential, corpoEnroll},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := envia(h, "POST", c.caminho, c.bearer, c.corpo)
			if r.Code != http.StatusUnauthorized || r.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("esperado 401 com desafio Bearer, veio %d %s", r.Code, r.Body)
			}
		})
	}
}

func TestTokenEsgotado(t *testing.T) {
	h, tok := ambiente(t) // 5 usos
	for range 5 {
		registra(t, h, tok)
	}
	corpo, _ := json.Marshal(protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{OSFamily: "linux"}})
	if r := envia(h, "POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusUnauthorized {
		t.Errorf("sexto registro com token de 5 usos: esperado 401, veio %d", r.Code)
	}
}

func TestRotaAntigaComIDNaURLNaoExisteMais(t *testing.T) {
	h, _ := ambiente(t)
	r := envia(h, "POST", "/api/v1/machines/qualquer-id/inventory", "", inventario(t, "1"))
	if r.Code != http.StatusMethodNotAllowed && r.Code != http.StatusNotFound {
		t.Errorf("o envio com ID escolhido pelo cliente foi removido: veio %d", r.Code)
	}
}

func TestRejeicoesDeConteudo(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)

	var mapa map[string]any
	_ = json.Unmarshal(inventario(t, "1"), &mapa)
	mapa["schema_version"] = 99
	schema99, _ := json.Marshal(mapa)
	mapa["schema_version"] = 1
	mapa["hash"] = "md5:abc"
	hashRuim, _ := json.Marshal(mapa)
	mapa["hash"] = "sha256:" + strings.Repeat("a", 64)
	mapa["campo_de_um_agente_mais_novo"] = 1
	comExtra, _ := json.Marshal(mapa)

	casos := []struct {
		nome   string
		corpo  []byte
		status int
		codigo string
	}{
		{"json malformado", []byte(`{"schema_version":`), 400, "invalid_json"},
		{"schema desconhecido", schema99, 422, "unsupported_schema"},
		{"hash fora do formato", hashRuim, 422, "invalid_hash"},
		{"campo desconhecido é aceito", comExtra, 201, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, c.corpo)
			var e protocol.ErrorResponse
			_ = json.Unmarshal(r.Body.Bytes(), &e)
			if r.Code != c.status || e.Error.Code != c.codigo {
				t.Errorf("esperado %d %q, veio %d %s", c.status, c.codigo, r.Code, r.Body)
			}
		})
	}
}

func TestContentTypeETamanho(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)

	req := httptest.NewRequest("POST", "/api/v1/agent/inventory", bytes.NewReader(inventario(t, "1")))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("Content-Type errado: esperado 415, veio %d", rec.Code)
	}

	enorme := []byte(`{"schema_version":1,"x":"` + strings.Repeat("a", 9<<20) + `"}`)
	if r := envia(h, "POST", "/api/v1/agent/inventory", m.Credential, enorme); r.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("corpo grande demais: esperado 413, veio %d", r.Code)
	}
}
