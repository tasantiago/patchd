package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func novoServidor() http.Handler {
	return api.New(store.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func envia(h http.Handler, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const caminhoInv = "/api/v1/machines/itom-teste/inventory"

func TestInventarioGravaDepoisInalteradoDepoisMuda(t *testing.T) {
	h := novoServidor()

	r := envia(h, "POST", caminhoInv, "application/json", inventario(t, "1.34.6-1"))
	if r.Code != http.StatusCreated || !strings.Contains(r.Body.String(), `"stored"`) {
		t.Fatalf("primeiro envio: %d %s", r.Code, r.Body)
	}
	if r.Header().Get("X-Request-ID") == "" {
		t.Error("toda resposta deveria trazer o X-Request-ID")
	}

	r = envia(h, "POST", caminhoInv, "application/json", inventario(t, "1.34.6-1"))
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"unchanged"`) {
		t.Errorf("mesmo conteúdo (outro collected_at): esperado 200 unchanged, veio %d %s", r.Code, r.Body)
	}

	r = envia(h, "POST", caminhoInv, "application/json", inventario(t, "1.34.6-1ubuntu0.1"))
	if r.Code != http.StatusCreated {
		t.Errorf("conteúdo novo: esperado 201, veio %d %s", r.Code, r.Body)
	}

	r = envia(h, "GET", caminhoInv, "", nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "1.34.6-1ubuntu0.1") {
		t.Errorf("leitura deveria devolver o último inventário: %d %s", r.Code, r.Body)
	}
}

func TestRejeicoes(t *testing.T) {
	h := novoServidor()
	valido := inventario(t, "1")

	var semSchema map[string]any
	_ = json.Unmarshal(valido, &semSchema)
	semSchema["schema_version"] = 99
	schema99, _ := json.Marshal(semSchema)
	semSchema["schema_version"] = 1
	semSchema["hash"] = "md5:abc"
	hashRuim, _ := json.Marshal(semSchema)

	enorme := []byte(`{"schema_version":1,"x":"` + strings.Repeat("a", 9<<20) + `"}`)

	casos := []struct {
		nome        string
		caminho     string
		contentType string
		corpo       []byte
		status      int
		codigo      string
	}{
		{"content-type errado", caminhoInv, "text/plain", valido, 415, "unsupported_media_type"},
		{"json malformado", caminhoInv, "application/json", []byte(`{"schema_version":`), 400, "invalid_json"},
		{"dados além do json", caminhoInv, "application/json", append(append([]byte{}, valido...), []byte(`{}`)...), 400, "invalid_json"},
		{"schema desconhecido", caminhoInv, "application/json", schema99, 422, "unsupported_schema"},
		{"hash fora do formato", caminhoInv, "application/json", hashRuim, 422, "invalid_hash"},
		{"corpo grande demais", caminhoInv, "application/json", enorme, 413, "body_too_large"},
		{"id inválido", "/api/v1/machines/-comeca-com-hifen/inventory", "application/json", valido, 400, "invalid_machine_id"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := envia(h, "POST", c.caminho, c.contentType, c.corpo)
			var e protocol.ErrorResponse
			_ = json.Unmarshal(r.Body.Bytes(), &e)
			if r.Code != c.status || e.Error.Code != c.codigo {
				t.Errorf("esperado %d %s, veio %d %s", c.status, c.codigo, r.Code, r.Body)
			}
		})
	}
}

func TestCampoDesconhecidoEAceito(t *testing.T) {
	var m map[string]any
	_ = json.Unmarshal(inventario(t, "1"), &m)
	m["campo_de_um_agente_mais_novo"] = map[string]any{"qualquer": 1}
	corpo, _ := json.Marshal(m)

	r := envia(novoServidor(), "POST", caminhoInv, "application/json", corpo)
	if r.Code != http.StatusCreated {
		t.Errorf("campos desconhecidos devem ser aceitos (compatibilidade com agentes mais novos): %d %s", r.Code, r.Body)
	}
}

func TestMaquinaDesconhecida(t *testing.T) {
	r := envia(novoServidor(), "GET", "/api/v1/machines/nunca-vista/inventory", "", nil)
	if r.Code != http.StatusNotFound {
		t.Errorf("esperado 404, veio %d", r.Code)
	}
}

func TestScanEListaDeMaquinas(t *testing.T) {
	h := novoServidor()
	envia(h, "POST", caminhoInv, "application/json", inventario(t, "1"))

	sim := true
	scan := protocol.PatchScanReport{
		SchemaVersion: protocol.PatchSchemaVersion,
		ScannedAt:     time.Now().UTC(),
		Scans:         []protocol.ScanResult{{Source: "apt", Missing: []protocol.MissingUpdate{}}},
		Reboot:        &protocol.RebootStatus{Pending: &sim},
	}
	corpo, _ := json.Marshal(scan)
	if r := envia(h, "POST", "/api/v1/machines/itom-teste/scan", "application/json", corpo); r.Code != http.StatusCreated {
		t.Fatalf("envio da busca: %d %s", r.Code, r.Body)
	}

	r := envia(h, "GET", "/api/v1/machines", "", nil)
	var lista []protocol.MachineSummary
	if err := json.Unmarshal(r.Body.Bytes(), &lista); err != nil || len(lista) != 1 {
		t.Fatalf("lista de máquinas: %v %s", err, r.Body)
	}
	m := lista[0]
	if m.Hostname != "itom-teste" || m.OSName != "Ubuntu" || m.ScanReceivedAt == nil || m.RebootPending == nil || !*m.RebootPending {
		t.Errorf("resumo errado: %+v", m)
	}
}

func TestMetodoNaoPermitido(t *testing.T) {
	r := envia(novoServidor(), "DELETE", caminhoInv, "", nil)
	if r.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE não existe nessa rota: esperado 405, veio %d", r.Code)
	}
}
