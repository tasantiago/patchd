package api_test

import (
	"bytes"
	"compress/gzip"
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
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// ambiente monta a API sobre a memória, com um token de enrollment de 5 usos e a sessão
// de painel sessaoTeste aberta (perfil leitura).
func ambiente(t *testing.T) (http.Handler, string) {
	t.Helper()
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	abreSessao(t, st, sessaoTeste, time.Now().UTC())
	return api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))), tok
}

// sessaoTeste é o cookie da sessão aberta por ambiente.
const sessaoTeste = panel.SessionPrefix + "sessao-de-teste"

// abreSessao grava direto no armazenamento uma sessão de leitura usada pela última vez em
// visto (sem passar pelo login, que confere o PBKDF2 e é lento).
func abreSessao(t *testing.T, st *store.Memory, cookie string, visto time.Time) {
	t.Helper()
	s := panel.Session{Username: "consulta", Role: panel.RoleRead, Source: panel.SourceLocal,
		CreatedAt: visto, ExpiresAt: visto.Add(panel.SessionMaxAge), LastSeenAt: visto}
	if err := st.CreatePanelSession(context.Background(), identity.Hash(cookie), s); err != nil {
		t.Fatal(err)
	}
}

// le faz um GET com o cookie da sessão de teste: as rotas de leitura exigem o painel.
func le(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: sessaoTeste})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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
	r = le(h, "/api/v1/machines/"+m.MachineID+"/inventory")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "libcares2") {
		t.Errorf("leitura pelo ID emitido: %d %s", r.Code, r.Body)
	}
	r = le(h, "/api/v1/machines")
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
	ra := le(h, "/api/v1/machines/"+a.MachineID+"/inventory")
	rb := le(h, "/api/v1/machines/"+b.MachineID+"/inventory")
	if !strings.Contains(ra.Body.String(), `"version":"A"`) || !strings.Contains(rb.Body.String(), `"version":"B"`) {
		t.Errorf("cada credencial grava na própria máquina:\nA: %s\nB: %s", ra.Body, rb.Body)
	}
}

func TestRecusasDeAutenticacao(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)
	inv := inventario(t, "1")
	corpoEnroll, _ := json.Marshal(protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{InstallID: "guid-2", OSFamily: "linux"}})

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
	corpo, _ := json.Marshal(protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{InstallID: "guid-2", OSFamily: "linux"}})
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

func TestEvidenciaInsuficiente(t *testing.T) {
	h, tok := ambiente(t)
	for nome, ev := range map[string]protocol.IdentityEvidence{
		"só o sistema":          {OSFamily: "linux", Hostname: "x"},
		"só valores de fábrica": {OSFamily: "linux", InstallID: "uninitialized", HardwareUUID: "00000000-0000-0000-0000-000000000000", Serial: "Default string"},
		"só o serial":           {OSFamily: "linux", Serial: "PF3ABC12"},
	} {
		t.Run(nome, func(t *testing.T) {
			corpo, _ := json.Marshal(protocol.EnrollRequest{Evidence: ev})
			r := envia(h, "POST", "/api/v1/enroll", tok, corpo)
			if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "insufficient_evidence") {
				t.Errorf("esperado 422 insufficient_evidence, veio %d %s", r.Code, r.Body)
			}
		})
	}
}

func TestAlertaDeIdentidadeNaoVazaParaOCliente(t *testing.T) {
	h, tok := ambiente(t)
	a := registra(t, h, tok)
	b := registra(t, h, tok) // mesmo install_id ("guid-1"): reenrollment de A
	r := le(h, "/api/v1/identity-links")
	var links []protocol.IdentityLink
	if err := json.Unmarshal(r.Body.Bytes(), &links); err != nil || len(links) != 1 {
		t.Fatalf("esperado um alerta: %v %s", err, r.Body)
	}
	l := links[0]
	if l.MachineID != b.MachineID || l.RelatedMachineID != a.MachineID || l.Relation != "reenrollment" {
		t.Errorf("alerta errado: %+v", l)
	}
}

func TestCredentialProblemNoLog(t *testing.T) {
	var buf bytes.Buffer
	h := api.New(store.NewMemory(), slog.New(slog.NewTextHandler(&buf, nil)))
	envia(h, "POST", "/api/v1/agent/inventory", "patchd_enr_qualquer", inventario(t, "1"))
	if !strings.Contains(buf.String(), "token de enrollment usado no lugar da credencial") {
		t.Errorf("o log deve dizer que o token de enrollment foi usado como credencial:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "patchd_enr_qualquer") {
		t.Error("o segredo não pode aparecer no log")
	}
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func enviaGzip(h http.Handler, path, bearer, encoding string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", encoding)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCorpoGzip(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)

	if r := enviaGzip(h, "/api/v1/agent/inventory", m.Credential, "gzip", gz(t, inventario(t, "1"))); r.Code != http.StatusCreated {
		t.Errorf("inventário em gzip: %d %s", r.Code, r.Body)
	}
	if r := enviaGzip(h, "/api/v1/agent/inventory", m.Credential, "gzip", []byte("não é gzip")); r.Code != http.StatusBadRequest ||
		!strings.Contains(r.Body.String(), "invalid_gzip") {
		t.Errorf("gzip inválido: %d %s", r.Code, r.Body)
	}
	if r := enviaGzip(h, "/api/v1/agent/inventory", m.Credential, "br", inventario(t, "1")); r.Code != http.StatusUnsupportedMediaType {
		t.Errorf("codificação desconhecida: %d %s", r.Code, r.Body)
	}

	// Bomba de compressão: 40 MB de espaços (JSON válido até ali) viram poucos KB de gzip.
	bomba := gz(t, append([]byte(`{"schema_version":1,"x":"`), bytes.Repeat([]byte(" "), 40<<20)...))
	if len(bomba) > 1<<20 {
		t.Fatalf("a bomba deveria ser pequena comprimida: %d bytes", len(bomba))
	}
	r := enviaGzip(h, "/api/v1/agent/inventory", m.Credential, "gzip", bomba)
	if r.Code != http.StatusRequestEntityTooLarge || !strings.Contains(r.Body.String(), "descomprimido") {
		t.Errorf("bomba de compressão: esperado 413, veio %d %s", r.Code, r.Body)
	}
}

func TestCheckin(t *testing.T) {
	h, tok := ambiente(t)
	m := registra(t, h, tok)
	inv := inventario(t, "1")
	var rep protocol.InventoryReport
	_ = json.Unmarshal(inv, &rep)

	checkin := func(hash string) (int, protocol.CheckinResponse) {
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v0.4.0", InventoryHash: hash, SentAt: time.Now().UTC()})
		r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return r.Code, resp
	}

	if code, resp := checkin(rep.Hash); code != http.StatusOK || resp.InventoryKnown {
		t.Errorf("antes do primeiro inventário, o servidor não o conhece: %d %+v", code, resp)
	}
	envia(h, "POST", "/api/v1/agent/inventory", m.Credential, inv)
	if code, resp := checkin(rep.Hash); code != http.StatusOK || !resp.InventoryKnown || resp.ServerTime.IsZero() {
		t.Errorf("depois do envio, o servidor conhece o hash: %d %+v", code, resp)
	}
	if _, resp := checkin("sha256:" + strings.Repeat("b", 64)); resp.InventoryKnown {
		t.Error("hash diferente: o agente precisa enviar o inventário")
	}
	if code, _ := checkin("md5:x"); code != http.StatusUnprocessableEntity {
		t.Errorf("hash fora do formato: esperado 422, veio %d", code)
	}
	corpo, _ := json.Marshal(protocol.CheckinRequest{InventoryHash: rep.Hash})
	if r := envia(h, "POST", "/api/v1/agent/checkin", "", corpo); r.Code != http.StatusUnauthorized {
		t.Errorf("check-in sem credencial: esperado 401, veio %d", r.Code)
	}
}
