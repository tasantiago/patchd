package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFormatoTexto(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("patchd_teste_total", "Contador de teste.\nSegunda linha.", "rota", "codigo")
	c.Inc("POST /api/v1/agent/checkin", "200")
	c.Inc("POST /api/v1/agent/checkin", "200")
	c.Add(0.5, `com "aspas" e \ barra`, "500")
	c.Add(-1, "x", "1") // negativo: ignorado
	r.NewGaugeFunc("patchd_medidor", "Medidor.", []string{"estado"}, func() []Sample {
		return []Sample{{Labels: []string{"b"}, Value: 2}, {Labels: []string{"a"}, Value: 1.5}, {Labels: []string{"malformado", "x"}, Value: 9}}
	})
	r.NewGaugeFunc("patchd_sem_rotulo", "Sem rótulo.", nil, func() []Sample { return []Sample{{Value: 1e10}} })
	var b strings.Builder
	if err := r.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	quer := `# HELP patchd_teste_total Contador de teste.\nSegunda linha.
# TYPE patchd_teste_total counter
patchd_teste_total{rota="POST /api/v1/agent/checkin",codigo="200"} 2
patchd_teste_total{rota="com \"aspas\" e \\ barra",codigo="500"} 0.5
# HELP patchd_medidor Medidor.
# TYPE patchd_medidor gauge
patchd_medidor{estado="a"} 1.5
patchd_medidor{estado="b"} 2
# HELP patchd_sem_rotulo Sem rótulo.
# TYPE patchd_sem_rotulo gauge
patchd_sem_rotulo 10000000000
`
	if b.String() != quer {
		t.Errorf("saída:\n%s\nquer:\n%s", b.String(), quer)
	}
	if c.Value("POST /api/v1/agent/checkin", "200") != 2 {
		t.Error("Value")
	}
	for _, ruim := range []func(){
		func() { r.NewCounter("patchd_teste_total", "repetido") },
		func() { r.NewCounter("nome-invalido", "x") },
		func() { r.NewCounter("patchd_x_total", "x", "__reservado") },
		func() { c.Inc("só um rótulo") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("devia entrar em pânico")
				}
			}()
			ruim()
		}()
	}
}

func TestHandlerComToken(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("patchd_x_total", "x").Inc()
	h := r.Handler("token-certo")
	pede := func(method, path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := pede("GET", "/metrics", "Bearer token-certo"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "patchd_x_total 1") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Errorf("com token: %d %q", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		method, path, auth string
		quer               int
	}{
		{"GET", "/metrics", "", http.StatusUnauthorized},
		{"GET", "/metrics", "Bearer token-errado", http.StatusUnauthorized},
		{"GET", "/metrics", "Basic dG9rZW4tY2VydG8=", http.StatusUnauthorized},
		{"POST", "/metrics", "Bearer token-certo", http.StatusMethodNotAllowed},
		{"GET", "/painel/", "Bearer token-certo", http.StatusNotFound},
	} {
		if rec := pede(c.method, c.path, c.auth); rec.Code != c.quer {
			t.Errorf("%s %s %q: %d (quer %d)", c.method, c.path, c.auth, rec.Code, c.quer)
		}
	}
	// Token vazio: nada abre (configuração errada não vira métrica aberta).
	vazio := r.Handler("")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer ")
	vazio.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token vazio: %d", rec.Code)
	}
}
