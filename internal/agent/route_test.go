package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// O relé automático: fora da rede, o agente disca o relé e o TLS continua conferindo o nome do
// servidor; um nome que o certificado não cobre é recusado mesmo pelo relé. De volta à rede,
// fala direto. A escolha vale por um minuto, e a mudança de rede (Invalidate) testa de novo.
func TestReleAutomatico(t *testing.T) {
	var hosts []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	// O certificado do httptest vale para example.com: é o "nome do servidor" aqui.
	base := "https://example.com:" + port

	r := NewRoute(nil)
	agora := time.Now()
	r.now = func() time.Time { return agora }
	fora := true
	probes := 0
	r.probe = func(_ context.Context, addr string) error {
		probes++
		if addr != "example.com:"+port {
			t.Errorf("testou %q", addr)
		}
		if fora {
			return errors.New("sem rota")
		}
		return nil
	}
	hc := &http.Client{Transport: r.Wrap(srv.Client().Transport), Timeout: 5 * time.Second}
	ctx := context.Background()

	// Sem relé: direto, sem testar nada.
	r.Choose(ctx, base)
	if r.ViaRelay() || probes != 0 {
		t.Fatal("sem relé, direto")
	}
	if r.SetRelay("https://x:1") || r.Relay() != "" {
		t.Error("endereço inválido aceito")
	}
	if !r.SetRelay(u.Host) || r.SetRelay(u.Host) {
		t.Fatal("SetRelay diz se mudou")
	}

	// Fora da rede: o servidor não responde direto, vai pelo relé, e o TLS confere example.com.
	r.Choose(ctx, base)
	if !r.ViaRelay() || probes != 1 {
		t.Fatalf("fora: via=%v probes=%d", r.ViaRelay(), probes)
	}
	resp, err := hc.Get(base + "/api/v1/agent/checkin")
	if err != nil {
		t.Fatalf("pelo relé: %v", err)
	}
	resp.Body.Close()
	if len(hosts) != 1 || hosts[0] != "example.com:"+port {
		t.Errorf("o servidor viu o host %v", hosts)
	}
	// Um nome que o certificado não cobre: recusado, mesmo discando o mesmo relé.
	if _, err := hc.Get("https://outro.exemplo:" + port + "/"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("nome errado pelo relé: %v", err)
	}

	// A escolha vale por um minuto; a mudança de rede testa de novo.
	r.Choose(ctx, base)
	if probes != 1 {
		t.Error("testou de novo antes do minuto")
	}
	fora = false
	r.Invalidate()
	r.Choose(ctx, base)
	if r.ViaRelay() || probes != 2 {
		t.Errorf("de volta à rede: via=%v probes=%d", r.ViaRelay(), probes)
	}
	agora = agora.Add(2 * time.Minute)
	fora = true
	r.Choose(ctx, base)
	if !r.ViaRelay() || probes != 3 {
		t.Errorf("depois do minuto: via=%v probes=%d", r.ViaRelay(), probes)
	}

	// Relé tirado pelo servidor: direto na hora.
	r.SetRelay("")
	if r.ViaRelay() {
		t.Error("relé tirado e ainda pelo relé")
	}
	// HTTP (porta de transição) não usa relé: ele só repassa a porta TLS dos agentes.
	r.SetRelay(u.Host)
	r.Choose(ctx, "http://example.com:8080")
	if r.ViaRelay() {
		t.Error("relé com HTTP")
	}
	// Rota nil (agente de teste, outros sistemas): tudo direto, sem pânico.
	var nada *Route
	nada.Choose(ctx, base)
	nada.Invalidate()
	if nada.SetRelay("x:1") || nada.ViaRelay() || nada.Relay() != "" {
		t.Error("rota nil")
	}
}

func TestEnderecoPrincipal(t *testing.T) {
	for in, quer := range map[string]string{
		"https://itom.exemplo.local:8443": "itom.exemplo.local:8443",
		"https://patchd.exemplo":          "patchd.exemplo:443",
		"https://[2001:db8::1]:8443/":     "[2001:db8::1]:8443",
		"http://patchd.exemplo:8080":      "",
	} {
		got, ok := primaryAddr(in)
		if got != quer || ok != (quer != "") {
			t.Errorf("%s: %q %v", in, got, ok)
		}
	}
}

// O -relay do install fica no estado (o serviço sobe sabendo do relé), sem trocar o que o
// servidor já mandou.
func TestSeedRelay(t *testing.T) {
	dir := t.TempDir()
	if err := SeedRelay(dir, "https://x"); err == nil {
		t.Error("endereço inválido aceito")
	}
	if err := SeedRelay(dir, "203.0.113.10:8443"); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadState(dir); s.Relay != "203.0.113.10:8443" {
		t.Fatalf("estado: %+v", s)
	}
	if err := SeedRelay(dir, "198.51.100.1:443"); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadState(dir); s.Relay != "203.0.113.10:8443" {
		t.Errorf("trocou o relé guardado: %q", s.Relay)
	}
}
