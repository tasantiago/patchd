package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// certDeTeste grava um certificado autoassinado para 127.0.0.1 (o laboratório usa uma CA de
// laboratório; aqui basta o par) e devolve o certificado para o cliente confiar.
func certDeTeste(t *testing.T, dir string, serial int64) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "patchd-teste"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(20 * 24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	_ = os.WriteFile(filepath.Join(dir, "srv.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "srv.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600)
	c, _ := x509.ParseCertificate(der)
	return c
}

// logSeguro: o log é escrito por quem atende e lido pelo teste.
type logSeguro struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logSeguro) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logSeguro) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func silencio() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCertificadoRelido(t *testing.T) {
	dir := t.TempDir()
	certDeTeste(t, dir, 1)
	c, err := newCertReloader(filepath.Join(dir, "srv.crt"), filepath.Join(dir, "srv.key"), silencio())
	if err != nil {
		t.Fatal(err)
	}
	agora := time.Now()
	c.now = func() time.Time { return agora }
	serial := func() int64 {
		cert, _ := c.GetCertificate(nil)
		return cert.Leaf.SerialNumber.Int64()
	}
	if serial() != 1 {
		t.Fatal("serial inicial")
	}
	if exp, nomes, cadeia := c.Info(); nomes != 1 || cadeia != 1 || time.Until(exp) > 21*24*time.Hour {
		t.Errorf("info: %v %d %d", exp, nomes, cadeia)
	}
	// Renovação: arquivos novos (mtime adiante). Antes dos 30 s, nada muda; depois, o novo.
	certDeTeste(t, dir, 2)
	futuro := agora.Add(time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "srv.crt"), futuro, futuro)
	_ = os.Chtimes(filepath.Join(dir, "srv.key"), futuro, futuro)
	if serial() != 1 {
		t.Error("releu antes do intervalo")
	}
	agora = agora.Add(31 * time.Second)
	if serial() != 2 {
		t.Error("não releu o certificado novo")
	}
	// Arquivo pela metade: o anterior continua.
	_ = os.WriteFile(filepath.Join(dir, "srv.crt"), []byte("-----BEGIN CERTIFICATE-----\nlixo"), 0o644)
	mais := futuro.Add(time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "srv.crt"), mais, mais)
	agora = agora.Add(31 * time.Second)
	if serial() != 2 {
		t.Error("um par ilegível não pode derrubar o TLS")
	}
	if _, err := newCertReloader(filepath.Join(dir, "srv.crt"), filepath.Join(dir, "srv.key"), silencio()); err == nil {
		t.Error("par ilegível na partida é erro")
	}
}

func portaLivre(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestHTTPSEPortaDeTransicao(t *testing.T) {
	dir := t.TempDir()
	cert := certDeTeste(t, dir, 1)
	c, err := newCertReloader(filepath.Join(dir, "srv.crt"), filepath.Join(dir, "srv.key"), silencio())
	if err != nil {
		t.Fatal(err)
	}
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "app "+r.URL.Path) })
	full := newHandler(app, nil)
	tlsAddr, httpAddr := portaLivre(t), portaLivre(t)
	registro := &logSeguro{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, silencio(), []listener{
			{name: "API", addr: tlsAddr, handler: withHSTS(full), tls: tlsConfig(c)},
			{name: "HTTP de transição", addr: httpAddr, handler: transitionHandler(full, slog.New(slog.NewTextHandler(registro, nil)), false)},
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("encerramento: %v", err)
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	cliente := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	pede := func(cl *http.Client, url string) (int, string, http.Header) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ { // a porta abre logo depois do go
			if resp, err = cl.Get(url); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	// HTTPS: tudo, com HSTS.
	if code, body, h := pede(cliente, "https://"+tlsAddr+"/painel/entrar"); code != 200 || body != "app /painel/entrar" || h.Get("Strict-Transport-Security") != hstsValue {
		t.Errorf("https painel: %d %q %v", code, body, h)
	}
	// HTTP de transição: agente, registro, saúde e cache sim; painel e leitura não.
	simples := &http.Client{Timeout: 3 * time.Second}
	for path, quer := range map[string]int{
		"/api/v1/agent/checkin": 200, "/api/v1/enroll": 200, "/healthz": 200,
		"/painel/entrar": 403, "/painel/": 403, "/api/v1/machines": 403, "/api/v1/compliance": 403,
	} {
		code, body, h := pede(simples, "http://"+httpAddr+path)
		if code != quer || h.Get("Strict-Transport-Security") != "" {
			t.Errorf("http %s: %d (quer %d) %q", path, code, quer, body)
		}
		if quer == 403 && !strings.Contains(body, "HTTPS") {
			t.Errorf("http %s: a recusa explica: %q", path, body)
		}
		// Recusa vai para o log (Aula 9.1b); o que passa, não (o log de requisições é outro).
		linha := "path=" + path + " "
		if got := strings.Contains(registro.String(), linha); got != (quer == 403) {
			t.Errorf("http %s: no log da transição = %v (quer %v)", path, got, quer == 403)
		}
	}
	// A query fica fora do log: pode carregar dados de formulário.
	_, _, _ = pede(simples, "http://"+httpAddr+"/painel/entrar?usuario=fulano")
	if strings.Contains(registro.String(), "fulano") {
		t.Error("a query apareceu no log da transição")
	}
	// TLS 1.1 é recusado no aperto de mão.
	velho := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS11}}}
	if _, err := velho.Get("https://" + tlsAddr + "/healthz"); err == nil {
		t.Error("TLS 1.1 aceito")
	}
	// O -healthcheck fala HTTPS com o servidor local.
	if err := healthcheck(tlsAddr, true); err != nil {
		t.Errorf("healthcheck: %v", err)
	}
	if err := healthcheck(tlsAddr, false); err == nil {
		t.Error("healthcheck em HTTP numa porta TLS")
	}
}

func TestConfiguracaoTLS(t *testing.T) {
	base := map[string]string{"PATCHD_CATALOG_INTERVAL": "0"}
	com := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for nome, c := range map[string]struct {
		env map[string]string
		msg string
	}{
		"só o certificado":  {com("PATCHD_TLS_CERT_FILE", "/x.crt"), "vão juntos"},
		"caminho relativo":  {com("PATCHD_TLS_CERT_FILE", "x.crt", "PATCHD_TLS_KEY_FILE", "/x.key"), "absolutos"},
		"transição sem TLS": {com("PATCHD_HTTP_LISTEN_ADDR", ":8081"), "só vale com TLS"},
		"mesma porta":       {com("PATCHD_TLS_CERT_FILE", "/x.crt", "PATCHD_TLS_KEY_FILE", "/x.key", "PATCHD_HTTP_LISTEN_ADDR", ":8080"), "diferentes"},
		// Aula 9.2: a URL pública é https e exige TLS neste servidor.
		"url pública sem TLS": {com("PATCHD_PUBLIC_URL", "https://patchd.exemplo:8443"), "exige TLS"},
		"url pública em http": {com("PATCHD_TLS_CERT_FILE", "/x.crt", "PATCHD_TLS_KEY_FILE", "/x.key", "PATCHD_LISTEN_ADDR", ":8443", "PATCHD_PUBLIC_URL", "http://patchd.exemplo:8080"), "https"},
	} {
		_, err := loadServerConfig(nil, lookMap(c.env), io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	cfg, err := loadServerConfig(nil, lookMap(com("PATCHD_TLS_CERT_FILE", "/x.crt", "PATCHD_TLS_KEY_FILE", "/x.key", "PATCHD_LISTEN_ADDR", ":8443",
		"PATCHD_HTTP_LISTEN_ADDR", ":8080")), io.Discard)
	if err != nil || !cfg.TLS() || cfg.HTTPListenAddr != ":8080" {
		t.Errorf("válida: %+v %v", cfg, err)
	}
	cfg, err = loadServerConfig(nil, lookMap(com("PATCHD_TLS_CERT_FILE", "/x.crt", "PATCHD_TLS_KEY_FILE", "/x.key", "PATCHD_LISTEN_ADDR", ":8443",
		"PATCHD_PUBLIC_URL", "HTTPS://Patchd.Exemplo:8443/")), io.Discard)
	if err != nil || cfg.PublicURL != "https://patchd.exemplo:8443" {
		t.Errorf("url pública normalizada: %q %v", cfg.PublicURL, err)
	}
	if u, _ := healthcheckURL(":8443", true); u != "https://127.0.0.1:8443/healthz" {
		t.Errorf("url: %s", u)
	}
}

// TestPortaSoDoCache (Aula 9.3): com a frota em HTTPS, a porta HTTP fica só para o cache;
// registro e rotas do agente também recebem 403, e cada recusa de agente vai para o log.
func TestPortaSoDoCache(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "app "+r.URL.Path) })
	registro := &logSeguro{}
	h := transitionHandler(app, slog.New(slog.NewTextHandler(registro, nil)), true)
	for path, quer := range map[string]int{
		"/healthz": 200, "/repo/stats": 200, "/repo/fedora/x": 200,
		"/api/v1/agent/checkin": 403, "/api/v1/enroll": 403, "/painel/": 403,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != quer {
			t.Errorf("%s: %d (quer %d) %q", path, rec.Code, quer, rec.Body.String())
		}
	}
	if !strings.Contains(registro.String(), "agente recusado") || !strings.Contains(registro.String(), "path=/api/v1/enroll") {
		t.Errorf("log:\n%s", registro.String())
	}
	// O proxy do apt (forma absoluta) continua passando.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "http://archive.ubuntu.com/ubuntu/dists/x", nil))
	if rec.Code != 200 {
		t.Errorf("proxy do apt: %d", rec.Code)
	}
	// Configuração: só com a porta HTTP.
	if _, err := loadServerConfig(nil, lookMap(map[string]string{"PATCHD_CATALOG_INTERVAL": "0", "PATCHD_HTTP_CACHE_ONLY": "true"}), io.Discard); err == nil ||
		!strings.Contains(err.Error(), "só com a porta HTTP") {
		t.Errorf("cache-only sem porta HTTP: %v", err)
	}
}
