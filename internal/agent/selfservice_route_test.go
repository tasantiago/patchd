package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// A Central de software fora da rede: o caminho é conferido na hora da chamada, sem esperar o
// ciclo. Na partida (relé guardado, nenhum check-in ainda) e depois de sair da rede (a escolha
// recente era "direto"), a loja, o pedido e o logotipo vão pelo relé.
func TestCentralPeloRele(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case protocol.SelfServiceLogoPath:
			_, _ = io.WriteString(w, "logo")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{}")
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	base := "https://example.com:" + port // o nome do certificado do httptest

	fora := true
	// O nome do servidor só "resolve" dentro da rede; o relé (o endereço do httptest) sempre.
	inner := srv.Client().Transport.(*http.Transport).Clone()
	inner.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != u.Host {
			if fora {
				return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: "example.com", IsNotFound: true}}
			}
			addr = u.Host
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	route := NewRoute(nil)
	agora := time.Now()
	route.now = func() time.Time { return agora }
	probes := 0
	route.probe = func(context.Context, string) error {
		probes++
		if fora {
			return errors.New("sem rota")
		}
		return nil
	}
	c := NewClient(base, "cred", "test")
	c.HTTP.Transport = route.Wrap(inner)
	a := &Agent{Client: c, Route: route}
	ctx := context.Background()

	// Partida fora da rede: relé guardado, sem check-in ainda.
	route.SetRelay(u.Host)
	if _, err := a.SelfServiceCatalog(ctx, protocol.SelfServiceUser{}); err != nil {
		t.Fatalf("loja na partida: %v", err)
	}
	if !route.ViaRelay() || probes != 1 {
		t.Fatalf("partida: via=%v probes=%d", route.ViaRelay(), probes)
	}

	// De volta à rede: o ciclo escolhe "direto".
	fora = false
	route.Invalidate()
	route.Choose(ctx, base)
	if route.ViaRelay() {
		t.Fatal("na rede, direto")
	}
	// Sai da rede antes do próximo ciclo: a escolha é recente, a chamada falha na conexão, o
	// caminho é testado de novo e a chamada repetida pelo relé.
	fora = true
	if _, err := a.SelfServiceRequest(ctx, protocol.SelfServiceRequest{}); err != nil {
		t.Fatalf("pedido depois de sair da rede: %v", err)
	}
	if !route.ViaRelay() {
		t.Fatal("não passou ao relé")
	}
	b, err := a.SelfServiceLogo(ctx, 1<<10)
	if err != nil || string(b) != "logo" {
		t.Fatalf("logotipo: %q %v", b, err)
	}

	// Sem relé, a falha de conexão volta como está (sem repetir).
	route.SetRelay("")
	if _, err := a.SelfServiceCatalog(ctx, protocol.SelfServiceUser{}); err == nil || !connectFailed(err) {
		t.Fatalf("sem relé: %v", err)
	}
}

// A Central só mostra o logotipo do painel se o agente o entrega (localapi.LogoBackend).
var _ interface {
	SelfServiceLogo(context.Context, int64) ([]byte, error)
} = (*Agent)(nil)
