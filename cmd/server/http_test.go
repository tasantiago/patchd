package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPedidosDeProxyNaoChegamNaAPI(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "api") })
	repo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "cache") })
	casos := []struct {
		nome      string
		repo      http.Handler
		method    string
		alvo      string
		status    int
		resultado string
	}{
		{"API normal", nil, "GET", "/api/v1/machines", 200, "api"},
		{"healthz", nil, "GET", "/healthz", 200, "ok"},
		{"proxy com o cache desligado", nil, "GET", "http://archive.ubuntu.com/ubuntu/dists/resolute/InRelease", 403, "desligado"},
		{"forma absoluta apontando para a API", nil, "GET", "http://outro.exemplo/api/v1/machines", 403, "desligado"},
		{"CONNECT com o cache desligado", nil, "CONNECT", "archive.ubuntu.com:443", 403, "desligado"},
		{"proxy com o cache ligado", repo, "GET", "http://archive.ubuntu.com/ubuntu/dists/resolute/InRelease", 200, "cache"},
		{"API com o cache ligado", repo, "GET", "/api/v1/machines", 200, "api"},
	}
	for _, c := range casos {
		h := newHandler(api, c.repo)
		req := httptest.NewRequest(c.method, c.alvo, nil)
		if c.method == "CONNECT" {
			req.URL.Path, req.URL.Host = "", c.alvo
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.resultado) {
			t.Errorf("%s: %d %q", c.nome, rec.Code, rec.Body)
		}
	}
}

func TestConfigDoCacheDeRepositorios(t *testing.T) {
	look := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	}
	cfg, err := loadServerConfig(nil, look(map[string]string{"PATCHD_REPO_CACHE_DIR": "/var/cache/patchd-repo"}), io.Discard)
	if err != nil || cfg.RepoCacheDir != "/var/cache/patchd-repo" || cfg.RepoCacheHosts != "archive.ubuntu.com,security.ubuntu.com" {
		t.Errorf("padrão: %+v %v", cfg, err)
	}
	for _, env := range []map[string]string{
		{"PATCHD_REPO_CACHE_DIR": "relativo"},
		{"PATCHD_REPO_CACHE_DIR": "/abs", "PATCHD_REPO_CACHE_HOSTS": "http://archive.ubuntu.com"},
	} {
		if _, err := loadServerConfig(nil, look(env), io.Discard); err == nil || !strings.Contains(err.Error(), "PATCHD_REPO_CACHE") {
			t.Errorf("%v: %v", env, err)
		}
	}
}
