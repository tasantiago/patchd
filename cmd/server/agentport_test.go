package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A porta só de agentes (docs/rele.md): as rotas do agente e o /healthz passam, marcadas
// "pelo relé"; painel, API de leitura e registro de máquina nova voltam 403, com o log
// das recusas no máximo uma vez por minuto.
func TestPortaSoDeAgentes(t *testing.T) {
	var marcadas []string
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marcadas = append(marcadas, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	var registro bytes.Buffer
	h := agentOnlyHandler(app, slog.New(slog.NewTextHandler(&registro, nil)))
	for _, p := range []string{"/api/v1/agent/checkin", "/api/v1/agent/canal", "/api/v1/agent/content/pacotes/x", "/healthz"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("POST", p, nil))
		if r.Code != http.StatusNoContent {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	if len(marcadas) != 4 { // antes do registro
		t.Fatalf("passaram: %v", marcadas)
	}
	// O registro passa (só com o token pelo relé, que o Enroll confere), mas só o POST.
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("POST", "/api/v1/enroll", nil))
	if r.Code != http.StatusNoContent || len(marcadas) != 5 {
		t.Errorf("registro pelo relé: %d %v", r.Code, marcadas)
	}
	for _, p := range []string{"/painel/entrar", "/api/v1/enroll", "/api/v1/machines", "/", "/api/v1/agentx", "/repo/ubuntu"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", p, nil))
		if r.Code != http.StatusForbidden {
			t.Errorf("%s: %d (deveria recusar)", p, r.Code)
		}
	}
	if n := strings.Count(registro.String(), "recusado na porta só de agentes"); n != 1 {
		t.Errorf("log das recusas: %d linha(s)\n%s", n, registro.String())
	}
	var l refusalLog
	agora := time.Now()
	l.allow(agora)
	l.allow(agora.Add(time.Second))
	l.allow(agora.Add(2 * time.Second))
	if ok, puladas := l.allow(agora.Add(61 * time.Second)); !ok || puladas != 2 {
		t.Errorf("depois de um minuto: %v %d", ok, puladas)
	}
}

func TestConfiguracaoPortaDeAgentes(t *testing.T) {
	tls := []string{"PATCHD_CATALOG_INTERVAL", "0", "PATCHD_TLS_CERT_FILE", "/x.crt", "PATCHD_TLS_KEY_FILE", "/x.key", "PATCHD_LISTEN_ADDR", ":8443"}
	env := func(kv ...string) map[string]string {
		m := map[string]string{"PATCHD_CATALOG_INTERVAL": "0"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for nome, c := range map[string]struct {
		env map[string]string
		msg string
	}{
		"sem TLS":            {env("PATCHD_AGENT_LISTEN_ADDR", ":9443"), "só vale com TLS"},
		"mesma da API":       {env(append(tls, "PATCHD_AGENT_LISTEN_ADDR", ":8443")...), "porta só dela"},
		"porta inválida":     {env(append(tls, "PATCHD_AGENT_LISTEN_ADDR", ":99999")...), "-agent-listen"},
		"mesma de transição": {env(append(tls, "PATCHD_HTTP_LISTEN_ADDR", ":8080", "PATCHD_AGENT_LISTEN_ADDR", ":8080")...), "porta só dela"},
	} {
		if _, err := loadServerConfig(nil, lookMap(c.env), io.Discard); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	cfg, err := loadServerConfig(nil, lookMap(env(append(tls, "PATCHD_AGENT_LISTEN_ADDR", ":9443")...)), io.Discard)
	if err != nil || cfg.AgentListenAddr != ":9443" {
		t.Errorf("válida: %+v %v", cfg.AgentListenAddr, err)
	}
}
